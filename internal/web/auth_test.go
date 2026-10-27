package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
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
	s := &Server{Store: st, Password: "correct horse", Location: time.UTC, Collector: http.NotFoundHandler()}
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
