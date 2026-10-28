// SPDX-License-Identifier: AGPL-3.0-only

// Package web serves the password-protected dashboard and static assets.
package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"math"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yi00it/bonestats/collect"
	"github.com/yi00it/bonestats/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// The login cookie. Over HTTPS it gets the __Host- prefix, which makes
// browsers insist on Secure, Path=/ and no Domain attribute.
const (
	cookieName       = "bs_auth"
	secureCookieName = "__Host-bs_auth"
)

const sessionLength = 30 * 24 * time.Hour

// maxForm caps the body of dashboard form posts, including the public login.
const maxForm = 64 << 10

var failDelay = time.Second

type Server struct {
	// Store holds the sites and their data, and the logins of the built-in
	// password login.
	Store       *store.Store
	Password    string
	ProxyHeader string // header the reverse proxy puts the client IP in (login rate limit)
	AllowHTTP   bool   // allow logging in over plain HTTP from other hosts than localhost
	Location    *time.Location
	Retention   int  // days, shown in the UI
	GeoEnabled  bool // shows the DB-IP attribution
	Collector   http.Handler
	SourceURL   string // linked in the footer, as the AGPL asks of network services

	// Authenticate, when set, replaces the built-in password login, for hosts
	// with their own accounts and one store per customer. It returns the
	// store of the user r is logged in as, or false. The host then serves
	// /login and /logout itself.
	Authenticate func(r *http.Request) (*store.Store, bool)

	// Links are extra entries for the header navigation, such as the
	// host's account page.
	Links []Link

	tmpl      map[string]*template.Template
	limiter   limiter
	proxyWarn sync.Once
}

// Link is an entry in the header navigation.
type Link struct{ Label, URL string }

func (s *Server) Handler() http.Handler {
	static, _ := fs.Sub(staticFS, "static")
	versions := staticVersions(static)
	staticURL := func(name string) string {
		if v, ok := versions[name]; ok {
			return "/static/" + name + "?v=" + v
		}
		return "/static/" + name
	}

	s.tmpl = map[string]*template.Template{}
	for _, page := range []string{"dashboard", "sites", "goals", "login"} {
		s.tmpl[page] = template.Must(template.New("").Funcs(funcs).Funcs(template.FuncMap{"static": staticURL, "source": func() string { return s.SourceURL }, "links": func() []Link { return s.Links }}).
			ParseFS(templateFS, "templates/layout.html", "templates/"+page+".html"))
	}

	files := http.FileServerFS(static)

	mux := http.NewServeMux()
	mux.Handle("/api/event", s.Collector)
	mux.Handle("GET /{script}", trackerHandler(static)) // bs.js and bs.<addons>.js
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheFor(24*time.Hour, files)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })

	// Dashboard forms reject cross-site POSTs (CSRF) via Sec-Fetch-Site/Origin,
	// and their bodies are capped.
	csrf := http.NewCrossOriginProtection()
	form := func(h http.Handler) http.Handler {
		return csrf.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, maxForm)
			h.ServeHTTP(w, r)
		}))
	}
	if s.Authenticate == nil {
		mux.HandleFunc("GET /login", s.loginPage)
		mux.Handle("POST /login", form(http.HandlerFunc(s.login)))
		mux.Handle("POST /logout", form(http.HandlerFunc(s.logout)))
	}
	mux.Handle("GET /{$}", s.auth(s.dashboard))
	mux.Handle("GET /sites", s.auth(s.sitesPage))
	mux.Handle("POST /sites", form(s.auth(s.addSite)))
	mux.Handle("POST /sites/{id}/delete", form(s.auth(s.deleteSite)))
	mux.Handle("GET /sites/{id}/goals", s.auth(s.goalsPage))
	mux.Handle("POST /sites/{id}/goals", form(s.auth(s.addGoal)))
	mux.Handle("POST /sites/{id}/goals/{gid}/delete", form(s.auth(s.deleteGoal)))
	mux.Handle("POST /sites/{id}/funnels", form(s.auth(s.addFunnel)))
	mux.Handle("POST /sites/{id}/funnels/{fid}/delete", form(s.auth(s.deleteFunnel)))
	return s.checkProxy(securityHeaders(mux))
}

// checkProxy logs once when requests arrive through a reverse proxy that
// bonestats wasn't told about: every client then seems to have the proxy's
// IP, so visitors are miscounted and one client's failed logins lock out
// everyone.
func (s *Server) checkProxy(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.ProxyHeader == "" && (r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("X-Real-IP") != "") {
			if ip := collect.ClientIP(r, ""); ip.IsLoopback() || ip.IsPrivate() {
				s.proxyWarn.Do(func() {
					log.Printf("warning: requests come through a reverse proxy (%s), but BONESTATS_PROXY_HEADER is not set; see the README", ip)
				})
			}
		}
		h.ServeHTTP(w, r)
	})
}

// ---- auth ----

// A login is a random token in the cookie; the server keeps only its hash, so
// logging out (or changing the password) ends it for good.
func tokenHash(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// https reports whether the browser reached us over HTTPS, directly or
// through a TLS-terminating proxy. A forged X-Forwarded-Proto only changes
// the cookie the forger gets, so it is harmless.
func https(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// localHost reports whether the dashboard was opened as localhost or a
// loopback address, where plain HTTP never leaves the machine.
func localHost(r *http.Request) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip, err := netip.ParseAddr(strings.Trim(host, "[]"))
	return err == nil && ip.IsLoopback()
}

func sessionCookieName(r *http.Request) string {
	if https(r) {
		return secureCookieName
	}
	return cookieName
}

func (s *Server) authenticated(r *http.Request) bool {
	c, err := r.Cookie(sessionCookieName(r))
	if err != nil || c.Value == "" {
		return false
	}
	ok, err := s.Store.LoginValid(r.Context(), tokenHash(c.Value))
	if err != nil {
		log.Printf("auth: %v", err)
	}
	return ok
}

type storeKey struct{}

// auth lets only logged-in users through, and gives the handler their store
// (see store).
func (s *Server) auth(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st, ok := s.Store, false
		if s.Authenticate != nil {
			st, ok = s.Authenticate(r)
		} else {
			ok = s.authenticated(r)
		}
		if !ok {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), storeKey{}, st)))
	})
}

// store returns the logged-in user's store.
func (s *Server) store(r *http.Request) *store.Store {
	return r.Context().Value(storeKey{}).(*store.Store)
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "login", map[string]any{"Title": "Log in"})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	// The password would cross the network in plain text.
	if !https(r) && !localHost(r) && !s.AllowHTTP {
		w.WriteHeader(http.StatusForbidden)
		s.render(w, "login", map[string]any{"Title": "Log in",
			"Error": "Log in over HTTPS. To allow plain HTTP on a trusted network, set BONESTATS_ALLOW_HTTP=true."})
		return
	}
	// Read the whole form before reserving an attempt, then compare.
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ip := collect.ClientIP(r, s.ProxyHeader)
	if !s.limiter.attempt(ip) {
		w.WriteHeader(http.StatusTooManyRequests)
		s.render(w, "login", map[string]any{"Title": "Log in", "Error": "Too many failed attempts. Try again in a few minutes."})
		return
	}
	pw := r.PostFormValue("password")
	if subtle.ConstantTimeCompare([]byte(pw), []byte(s.Password)) != 1 {
		time.Sleep(failDelay) // slow down guessing
		w.WriteHeader(http.StatusUnauthorized)
		s.render(w, "login", map[string]any{"Title": "Log in", "Error": "Wrong password."})
		return
	}
	s.limiter.success(ip)
	token := rand.Text()
	if err := s.Store.AddLogin(r.Context(), tokenHash(token), time.Now().Add(sessionLength)); err != nil {
		serverError(w, err)
		return
	}
	http.SetCookie(w, s.cookie(r, token, int(sessionLength.Seconds())))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName(r)); err == nil && c.Value != "" {
		if err := s.Store.DeleteLogin(r.Context(), tokenHash(c.Value)); err != nil {
			serverError(w, err)
			return
		}
	}
	http.SetCookie(w, s.cookie(r, "", -1))
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) cookie(r *http.Request, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name: sessionCookieName(r), Value: value, Path: "/", MaxAge: maxAge, HttpOnly: true,
		Secure: https(r), SameSite: http.SameSiteStrictMode,
	}
}

// Messages for the ?error= codes of the sites and goals pages. Only these
// fixed texts can be shown, so a crafted link can't put words in the UI.
var errorMessages = map[string]string{
	"exists":       "That already exists.",
	"domain":       "Enter a valid domain such as example.com (internationalized domains in their xn-- form).",
	"goal-kind":    store.ErrGoalKind.Error() + ".",
	"event-name":   "The event name is too long.",
	"goal-path":    "Page paths start with / and are at most 512 characters.",
	"currency":     "Revenue needs a 3-letter currency code and an event goal.",
	"funnel-name":  "A funnel needs a name of at most 100 characters.",
	"funnel-steps": "A funnel needs 2 to 8 steps.",
	"failed":       "That didn't work. The server log has the details.",
}

// errorCode maps an error from the store to one of errorMessages.
func errorCode(err error) string {
	for code, e := range map[string]error{
		"domain": store.ErrInvalidDomain, "goal-kind": store.ErrGoalKind, "event-name": store.ErrEventName,
		"goal-path": store.ErrGoalPath, "currency": store.ErrCurrency,
		"funnel-name": store.ErrFunnelName, "funnel-steps": store.ErrFunnelSteps,
	} {
		if errors.Is(err, e) {
			return code
		}
	}
	if strings.Contains(err.Error(), "UNIQUE") {
		return "exists"
	}
	log.Printf("error: %v", err)
	return "failed"
}

// ---- sites ----

func (s *Server) sitesPage(w http.ResponseWriter, r *http.Request) {
	sites, err := s.store(r).Sites(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	scheme := "http"
	if https(r) {
		scheme = "https"
	}
	s.render(w, "sites", map[string]any{
		"Title":     "Sites",
		"Sites":     sites,
		"BaseURL":   scheme + "://" + r.Host,
		"Error":     errorMessages[r.URL.Query().Get("error")],
		"Retention": s.Retention,
	})
}

func (s *Server) addSite(w http.ResponseWriter, r *http.Request) {
	if err := s.store(r).AddSite(r.Context(), r.PostFormValue("domain")); err != nil {
		http.Redirect(w, r, "/sites?error="+errorCode(err), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/sites", http.StatusSeeOther)
}

func (s *Server) deleteSite(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store(r).DeleteSite(r.Context(), id); err != nil {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, "/sites", http.StatusSeeOther)
}

// ---- template helpers ----

var funcs = template.FuncMap{
	"num":   formatNum,
	"pct":   func(f float64) string { return strconv.FormatFloat(f, 'f', 0, 64) + "%" },
	"pct1":  func(f float64) string { return strconv.FormatFloat(f, 'f', 1, 64) + "%" },
	"float": func(f float64) string { return strconv.FormatFloat(f, 'f', 1, 64) },
	"dur":   formatDur,
	"vital": func(key string, v float64) string {
		if metaFor(key).Unitless {
			return strconv.FormatFloat(v, 'f', 3, 64)
		}
		if v >= 1000 {
			return strconv.FormatFloat(v/1000, 'f', 2, 64) + " s"
		}
		return strconv.FormatFloat(math.Round(v), 'f', 0, 64) + " ms"
	},
	"rating": func(key string, v float64) string {
		m := metaFor(key)
		switch {
		case v <= m.Good:
			return "good"
		case v <= m.Poor:
			return "ni"
		default:
			return "poor"
		}
	},
	"vitalPos": vitalPos,
	"deref":    func(p *float64) float64 { return *p },
	"money":    money,
	"dict": func(kv ...any) map[string]any {
		m := map[string]any{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	},
}

func formatNum(n int64) string {
	f := float64(n)
	switch {
	case f >= 1e6:
		return strconv.FormatFloat(f/1e6, 'f', 1, 64) + "M"
	case f >= 1e4:
		return strconv.FormatFloat(f/1e3, 'f', 1, 64) + "k"
	default:
		return strconv.FormatInt(n, 10)
	}
}

func formatDur(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
}

func (s *Server) render(w http.ResponseWriter, page string, data map[string]any) {
	data["Year"] = time.Now().Year()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl[page].ExecuteTemplate(w, "layout", data); err != nil {
		log.Printf("render %s: %v", page, err)
	}
}

func serverError(w http.ResponseWriter, err error) {
	log.Printf("error: %v", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// staticVersions hashes each static file, so asset URLs change with their
// content and browsers never keep an outdated copy from their cache.
func staticVersions(fsys fs.FS) map[string]string {
	versions := map[string]string{}
	fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		h := sha256.Sum256(b)
		versions[path] = hex.EncodeToString(h[:4])
		return nil
	})
	return versions
}

func cacheFor(d time.Duration, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(d.Seconds())))
		h.ServeHTTP(w, r)
	})
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "same-origin")
		if r.URL.Path != "/api/event" && !strings.HasPrefix(r.URL.Path, "/bs.") {
			hd.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'")
			hd.Set("X-Frame-Options", "DENY")
		}
		h.ServeHTTP(w, r)
	})
}
