// SPDX-License-Identifier: AGPL-3.0-only

package report

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"net/url"
	"strings"
	"time"
)

// A Message is one email.
type Message struct {
	To             string
	Subject        string
	Text, HTML     string
	UnsubscribeURL string // for the List-Unsubscribe header (RFC 8058)
}

// A Mailer sends email.
type Mailer interface {
	Send(ctx context.Context, m Message) error
}

// SMTP sends email through an SMTP server.
type SMTP struct {
	host     string // host:port
	implicit bool   // TLS from the start (smtps, port 465) instead of STARTTLS
	user     string
	password string
	from     mail.Address
}

// NewSMTP parses an SMTP URL and a From address:
//
//	smtp://user:password@smtp.example.com:587  (STARTTLS when offered)
//	smtps://user:password@smtp.example.com:465 (TLS from the start)
//	smtp://relay:25                            (no login)
//
// Characters like @ or : in the user name or password must be
// percent-encoded. The password is only sent over TLS, or to localhost.
func NewSMTP(rawURL, from string) (*SMTP, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || (u.Scheme != "smtp" && u.Scheme != "smtps") {
		return nil, errors.New("the SMTP URL must look like smtp://user:password@host:587 or smtps://user:password@host:465")
	}
	a, err := mail.ParseAddress(from)
	if err != nil {
		return nil, fmt.Errorf("the From address %q isn't an email address", from)
	}
	m := &SMTP{host: u.Host, implicit: u.Scheme == "smtps", from: *a}
	if u.Port() == "" {
		port := "587"
		if m.implicit {
			port = "465"
		}
		m.host = net.JoinHostPort(u.Hostname(), port)
	}
	if u.User != nil {
		m.user = u.User.Username()
		m.password, _ = u.User.Password()
	}
	return m, nil
}

// Server returns host:port, for logs.
func (m *SMTP) Server() string { return m.host }

const smtpTimeout = time.Minute

func (m *SMTP) Send(ctx context.Context, msg Message) error {
	body, err := m.compose(msg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, smtpTimeout)
	defer cancel()
	host, _, _ := net.SplitHostPort(m.host)
	tlsConf := &tls.Config{ServerName: host}
	var conn net.Conn
	if m.implicit {
		conn, err = (&tls.Dialer{Config: tlsConf}).DialContext(ctx, "tcp", m.host)
	} else {
		conn, err = (&net.Dialer{}).DialContext(ctx, "tcp", m.host)
	}
	if err != nil {
		return err
	}
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if !m.implicit {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(tlsConf); err != nil {
				return err
			}
		}
	}
	if m.user != "" {
		// PlainAuth refuses to send the password without TLS, except to
		// localhost.
		if err := c.Auth(smtp.PlainAuth("", m.user, m.password, host)); err != nil {
			return err
		}
	}
	if err := c.Mail(m.from.Address); err != nil {
		return err
	}
	if err := c.Rcpt(msg.To); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(body); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// compose builds a multipart/alternative message with a text and an HTML
// part.
func (m *SMTP) compose(msg Message) ([]byte, error) {
	if strings.ContainsAny(msg.To, "\r\n") || strings.ContainsAny(msg.UnsubscribeURL, "\r\n<>") {
		return nil, errors.New("invalid header value")
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	id := make([]byte, 16)
	rand.Read(id)
	_, domain, _ := strings.Cut(m.from.Address, "@")
	h := []string{
		"From: " + m.from.String(),
		"To: " + msg.To,
		"Subject: " + mime.QEncoding.Encode("utf-8", msg.Subject),
		"Date: " + time.Now().Format(time.RFC1123Z),
		"Message-ID: <" + hex.EncodeToString(id) + "@" + domain + ">",
		"MIME-Version: 1.0",
		"Content-Type: multipart/alternative; boundary=" + mw.Boundary(),
		"Auto-Submitted: auto-generated",
	}
	if msg.UnsubscribeURL != "" {
		h = append(h, "List-Unsubscribe: <"+msg.UnsubscribeURL+">", "List-Unsubscribe-Post: List-Unsubscribe=One-Click")
	}
	buf.WriteString(strings.Join(h, "\r\n") + "\r\n\r\n")
	for _, part := range []struct{ typ, body string }{{"text/plain", msg.Text}, {"text/html", msg.HTML}} {
		pw, err := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {part.typ + "; charset=utf-8"},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return nil, err
		}
		qw := quotedprintable.NewWriter(pw)
		qw.Write([]byte(strings.ReplaceAll(part.body, "\n", "\r\n")))
		qw.Close()
	}
	mw.Close()
	return buf.Bytes(), nil
}
