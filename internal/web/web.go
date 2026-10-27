// Package web serves the password-protected dashboard and static assets.
package web

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"bonestats/internal/collect"
	"bonestats/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

const cookieName = "bs_auth"
const sessionLength = 30 * 24 * time.Hour

var failDelay = time.Second

type Server struct {
	Store      *store.Store
	Password   string
	TrustProxy bool // read the client IP from X-Forwarded-For (login rate limit)
	Location   *time.Location
	Retention  int  // days, shown in the UI
	GeoEnabled bool // shows the DB-IP attribution
	Collector  http.Handler

	tmpl    map[string]*template.Template
	limiter limiter
}

func (s *Server) Handler() http.Handler {
	s.tmpl = map[string]*template.Template{}
	for _, page := range []string{"dashboard", "sites", "goals", "login"} {
		s.tmpl[page] = template.Must(template.New("").Funcs(funcs).
			ParseFS(templateFS, "templates/layout.html", "templates/"+page+".html"))
	}

	static, _ := fs.Sub(staticFS, "static")
	files := http.FileServerFS(static)

	mux := http.NewServeMux()
	mux.Handle("/api/event", s.Collector)
	mux.Handle("GET /{script}", trackerHandler(static)) // bs.js and bs.<addons>.js
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheFor(24*time.Hour, files)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })

	// Dashboard forms reject cross-site POSTs (CSRF) via Sec-Fetch-Site/Origin.
	csrf := http.NewCrossOriginProtection()
	mux.HandleFunc("GET /login", s.loginPage)
	mux.Handle("POST /login", csrf.Handler(http.HandlerFunc(s.login)))
	mux.Handle("POST /logout", csrf.Handler(http.HandlerFunc(s.logout)))
	mux.Handle("GET /{$}", s.auth(s.dashboard))
	mux.Handle("GET /sites", s.auth(s.sitesPage))
	mux.Handle("POST /sites", csrf.Handler(s.auth(s.addSite)))
	mux.Handle("POST /sites/{id}/delete", csrf.Handler(s.auth(s.deleteSite)))
	mux.Handle("GET /sites/{id}/goals", s.auth(s.goalsPage))
	mux.Handle("POST /sites/{id}/goals", csrf.Handler(s.auth(s.addGoal)))
	mux.Handle("POST /sites/{id}/goals/{gid}/delete", csrf.Handler(s.auth(s.deleteGoal)))
	mux.Handle("POST /sites/{id}/funnels", csrf.Handler(s.auth(s.addFunnel)))
	mux.Handle("POST /sites/{id}/funnels/{fid}/delete", csrf.Handler(s.auth(s.deleteFunnel)))
	return securityHeaders(mux)
}

// ---- auth ----

// A login is a random token in the cookie; the server keeps only its hash, so
// logging out (or changing the password) ends it for good.
func tokenHash(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

func (s *Server) authenticated(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return false
	}
	ok, err := s.Store.LoginValid(r.Context(), tokenHash(c.Value))
	if err != nil {
		log.Printf("auth: %v", err)
	}
	return ok
}

func (s *Server) auth(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authenticated(r) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		h(w, r)
	})
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "login", map[string]any{"Title": "Log in"})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ip := collect.ClientIP(r, s.TrustProxy)
	if s.limiter.blocked(ip) {
		w.WriteHeader(http.StatusTooManyRequests)
		s.render(w, "login", map[string]any{"Title": "Log in", "Error": "Too many failed attempts. Try again in a few minutes."})
		return
	}
	pw := r.PostFormValue("password")
	if subtle.ConstantTimeCompare([]byte(pw), []byte(s.Password)) != 1 {
		s.limiter.fail(ip)
		time.Sleep(failDelay) // slow down guessing
		w.WriteHeader(http.StatusUnauthorized)
		s.render(w, "login", map[string]any{"Title": "Log in", "Error": "Wrong password."})
		return
	}
	s.limiter.reset(ip)
	token := rand.Text()
	if err := s.Store.AddLogin(r.Context(), tokenHash(token), time.Now().Add(sessionLength)); err != nil {
		serverError(w, err)
		return
	}
	http.SetCookie(w, s.cookie(r, token, int(sessionLength.Seconds())))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil && c.Value != "" {
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
		Name: cookieName, Value: value, Path: "/", MaxAge: maxAge, HttpOnly: true,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteStrictMode,
	}
}

// ---- sites ----

func (s *Server) sitesPage(w http.ResponseWriter, r *http.Request) {
	sites, err := s.Store.Sites(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	s.render(w, "sites", map[string]any{
		"Title":     "Sites",
		"Sites":     sites,
		"BaseURL":   scheme + "://" + r.Host,
		"Error":     r.URL.Query().Get("error"),
		"Retention": s.Retention,
	})
}

func (s *Server) addSite(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.AddSite(r.Context(), r.PostFormValue("domain")); err != nil {
		msg := "Could not add site: " + err.Error()
		if strings.Contains(err.Error(), "UNIQUE") {
			msg = "That site already exists."
		}
		http.Redirect(w, r, "/sites?error="+url.QueryEscape(msg), http.StatusSeeOther)
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
	if err := s.Store.DeleteSite(r.Context(), id); err != nil {
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
	"deref": func(p *float64) float64 { return *p },
	"money": money,
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
