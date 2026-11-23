// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yi00it/bonestats/collect"
	"github.com/yi00it/bonestats/store"
)

// Shared link passwords are stored as PBKDF2-SHA256 hashes, as
// "pbkdf2-sha256$<iterations>$<salt>$<hash>" in hex.
var sharePasswordIter = 600_000

// minSharePassword is the shortest password a shared link accepts.
const minSharePassword = 8

const shareCookie = "bs_share"

// shareSession is how long a visitor stays in after entering the password.
const shareSession = 30 * 24 * time.Hour

func hashSharePassword(password string) (string, error) {
	salt := make([]byte, 16)
	rand.Read(salt)
	key, err := pbkdf2.Key(sha256.New, password, salt, sharePasswordIter, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%x$%x", sharePasswordIter, salt, key), nil
}

func checkSharePassword(hash, password string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iter, err1 := strconv.Atoi(parts[1])
	salt, err2 := hex.DecodeString(parts[2])
	want, err3 := hex.DecodeString(parts[3])
	if err1 != nil || err2 != nil || err3 != nil || iter < 1 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iter, len(want))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

// sharePass is the cookie value that lets a visitor into a protected link.
// It is tied to the link's current password hash, so changing or removing
// the password, or deleting the link, ends every visitor's access.
func sharePass(ctx context.Context, st *store.Store, sh store.Share) (string, error) {
	secret, err := st.Secret(ctx, "share_secret")
	if err != nil {
		return "", err
	}
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(sh.Token + "\x00" + sh.Password))
	return hex.EncodeToString(m.Sum(nil)), nil
}

func shareCookiePath(sh store.Share) string { return "/share/" + sh.Token }

// shareUnlocked reports whether the request may see a protected link.
func shareUnlocked(r *http.Request, st *store.Store, sh store.Share) (bool, error) {
	if sh.Password == "" {
		return true, nil
	}
	c, err := r.Cookie(shareCookie)
	if err != nil {
		return false, nil
	}
	want, err := sharePass(r.Context(), st, sh)
	if err != nil {
		return false, err
	}
	return hmac.Equal([]byte(c.Value), []byte(want)), nil
}

func (s *Server) renderSharePassword(w http.ResponseWriter, sh store.Share, site store.Site, status int, msg string) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(status)
	s.render(w, "login", map[string]any{
		"Title":   site.Domain,
		"Action":  shareCookiePath(sh),
		"Tagline": site.Domain,
		"Label":   "Password",
		"Button":  "View statistics",
		"Error":   msg,
	})
}

// unlockShare checks a protected link's password and lets the visitor in.
func (s *Server) unlockShare(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	st, sh, site, ok := s.findShare(w, r)
	if !ok {
		return
	}
	if sh.Password == "" {
		http.Redirect(w, r, shareCookiePath(sh), http.StatusSeeOther)
		return
	}
	if !https(r) && !localHost(r) && !s.AllowHTTP {
		s.renderSharePassword(w, sh, site, http.StatusForbidden, "Passwords are only accepted over HTTPS.")
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// Its own limiter, so guessing a link's password can't lock the admin
	// out of the login.
	ip := collect.ClientIP(r, s.ProxyHeader)
	if !s.shareLimiter.attempt(ip) {
		s.renderSharePassword(w, sh, site, http.StatusTooManyRequests, "Too many failed attempts. Try again in a few minutes.")
		return
	}
	if !checkSharePassword(sh.Password, r.PostFormValue("password")) {
		time.Sleep(failDelay)
		s.renderSharePassword(w, sh, site, http.StatusUnauthorized, "Wrong password.")
		return
	}
	s.shareLimiter.success(ip)
	pass, err := sharePass(r.Context(), st, sh)
	if err != nil {
		serverError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: shareCookie, Value: pass, Path: shareCookiePath(sh), MaxAge: int(shareSession.Seconds()),
		HttpOnly: true, Secure: https(r), SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, shareCookiePath(sh), http.StatusSeeOther)
}

func (s *Server) setSharePassword(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteFromPath(w, r)
	if !ok {
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("sid"), 10, 64)
	hash := ""
	if r.PostFormValue("remove") == "" {
		pw := r.PostFormValue("password")
		if len(pw) < minSharePassword || len(pw) > 200 {
			http.Redirect(w, r, sharesURL(site)+"?error=share-password", http.StatusSeeOther)
			return
		}
		var err error
		if hash, err = hashSharePassword(pw); err != nil {
			serverError(w, err)
			return
		}
	}
	if err := s.store(r).SetSharePassword(r.Context(), site.ID, id, hash); err != nil {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, sharesURL(site), http.StatusSeeOther)
}
