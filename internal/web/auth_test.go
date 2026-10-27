package web

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"bonestats/internal/store"
)

func testServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	failDelay = 0
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := &Server{Store: st, Password: "correct horse", AllowHTTP: true, Location: time.UTC, Collector: http.NotFoundHandler()}
	return s, s.Handler()
}

func post(h http.Handler, path, form string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = "192.0.2.1:1234"
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func loggedIn(h http.Handler, c *http.Cookie) bool {
	r := httptest.NewRequest("GET", "/sites", nil)
	r.AddCookie(c)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code == http.StatusOK
}

func sessionCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == cookieName && c.Value != "" {
			return c
		}
	}
	t.Fatalf("no session cookie (status %d)", w.Code)
	return nil
}

func TestLogoutEndsSession(t *testing.T) {
	_, h := testServer(t)
	c := sessionCookie(t, post(h, "/login", "password="+url.QueryEscape("correct horse")))
	if !loggedIn(h, c) {
		t.Fatal("fresh login rejected")
	}
	post(h, "/logout", "", c)
	if loggedIn(h, c) {
		t.Error("cookie still works after logout")
	}
	if loggedIn(h, &http.Cookie{Name: cookieName, Value: "forged"}) {
		t.Error("forged cookie accepted")
	}
}

func TestPasswordChangeEndsSessions(t *testing.T) {
	s, h := testServer(t)
	ctx := context.Background()
	s.Store.CheckPassword(ctx, []byte("old"))
	c := sessionCookie(t, post(h, "/login", "password="+url.QueryEscape("correct horse")))
	if changed, _ := s.Store.CheckPassword(ctx, []byte("old")); changed || !loggedIn(h, c) {
		t.Fatal("unchanged password ended the session")
	}
	if changed, _ := s.Store.CheckPassword(ctx, []byte("new")); !changed || loggedIn(h, c) {
		t.Error("session survived a password change")
	}
}

func TestLoginRateLimit(t *testing.T) {
	_, h := testServer(t)
	for i := 0; i < maxFails; i++ {
		if w := post(h, "/login", "password=wrong"); w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status %d", i, w.Code)
		}
	}
	if w := post(h, "/login", "password="+url.QueryEscape("correct horse")); w.Code != http.StatusTooManyRequests {
		t.Errorf("locked-out client got status %d", w.Code)
	}
}

// Requests whose bodies arrive together must not all get past the lockout
// check before any failure is counted.
func TestLoginRateLimitConcurrent(t *testing.T) {
	_, h := testServer(t)
	const n = 100
	var wg sync.WaitGroup
	writers := make([]*io.PipeWriter, n)
	codes := make(chan int, n)
	for i := range writers {
		pr, pw := io.Pipe()
		writers[i] = pw
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest("POST", "/login", pr)
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.RemoteAddr = "192.0.2.1:1234"
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			codes <- w.Code
		}()
	}
	time.Sleep(100 * time.Millisecond) // let every handler block on its body
	for _, pw := range writers {
		pw.Write([]byte("password=wrong"))
		pw.Close()
	}
	wg.Wait()
	close(codes)
	checked := 0
	for c := range codes {
		if c == http.StatusUnauthorized {
			checked++
		}
	}
	if checked > maxFails {
		t.Errorf("%d passwords were checked, limit is %d", checked, maxFails)
	}
}

func TestLoginGlobalLimit(t *testing.T) {
	_, h := testServer(t)
	for i := 0; i < maxGlobalFails; i++ {
		r := httptest.NewRequest("POST", "/login", strings.NewReader("password=wrong"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.RemoteAddr = fmt.Sprintf("198.51.100.%d:1234", i/maxFails)
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	r := httptest.NewRequest("POST", "/login", strings.NewReader("password=wrong"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = "203.0.113.50:1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("fresh client after %d failures elsewhere got status %d", maxGlobalFails, w.Code)
	}
}

func TestLoginNeedsHTTPS(t *testing.T) {
	s, h := testServer(t)
	s.AllowHTTP = false
	login := func(host string, https bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://"+host+"/login", strings.NewReader("password="+url.QueryEscape("correct horse")))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if https {
			r.Header.Set("X-Forwarded-Proto", "https")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := login("stats.example.com", false); w.Code != http.StatusForbidden {
		t.Errorf("plain HTTP login: status %d", w.Code)
	}
	for _, host := range []string{"localhost:8080", "127.0.0.1:8080", "[::1]:8080"} {
		if w := login(host, false); w.Code != http.StatusSeeOther {
			t.Errorf("%s: status %d", host, w.Code)
		}
	}
	w := login("stats.example.com", true)
	var c *http.Cookie
	for _, k := range w.Result().Cookies() {
		if k.Name == secureCookieName {
			c = k
		}
	}
	if c == nil || !c.Secure {
		t.Fatalf("HTTPS login cookie = %+v", c)
	}
}

func TestErrorMessagesAreFixed(t *testing.T) {
	_, h := testServer(t)
	c := sessionCookie(t, post(h, "/login", "password="+url.QueryEscape("correct horse")))
	get := func(path string) string {
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(c)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Body.String()
	}
	if body := get("/sites?error=" + url.QueryEscape("Session expired, log in at evil.example")); strings.Contains(body, "evil.example") {
		t.Error("arbitrary error text shown")
	}
	w := post(h, "/sites", "domain="+url.QueryEscape("<b>x</b>"), c)
	if loc := w.Header().Get("Location"); loc != "/sites?error=domain" {
		t.Errorf("invalid domain redirected to %q", loc)
	}
	if body := get("/sites?error=domain"); !strings.Contains(body, "valid domain") {
		t.Error("known error code not shown")
	}
}

func TestLoginBodyLimit(t *testing.T) {
	_, h := testServer(t)
	w := post(h, "/login", "password="+strings.Repeat("a", maxForm))
	if w.Code != http.StatusBadRequest {
		t.Errorf("oversized login body: status %d", w.Code)
	}
}

// Asset URLs carry a content hash, so browsers fetch new versions at once.
func TestStaticVersions(t *testing.T) {
	_, h := testServer(t)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/login", nil))
	body := w.Body.String()
	i := strings.Index(body, `/static/app.css?v=`)
	if i < 0 {
		t.Fatalf("login page has no versioned app.css link:\n%s", body)
	}
	link := body[i : i+strings.IndexByte(body[i:], '"')]
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", link, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d", link, w.Code)
	}
}
