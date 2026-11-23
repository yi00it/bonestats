// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"archive/zip"
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func upload(h http.Handler, path, format string, file []byte, c *http.Cookie) *httptest.ResponseRecorder {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField("format", format)
	fw, _ := mw.CreateFormFile("file", "export.zip")
	fw.Write(file)
	mw.Close()
	r := httptest.NewRequest("POST", path, &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.RemoteAddr = "192.0.2.1:1234"
	if c != nil {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func plausibleExport(day string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, _ := zw.Create("imported_visitors_20260101_20260101.csv")
	f.Write([]byte("date,visitors,pageviews,bounces,visits,visit_duration\n" + day + ",1234,4000,10,1500,9000\n"))
	f, _ = zw.Create("imported_pages_20260101_20260101.csv")
	f.Write([]byte("date,hostname,page,visits,visitors,pageviews\n" + day + ",example.com,/imported-page,10,10,4000\n"))
	zw.Close()
	return buf.Bytes()
}

func TestImportPage(t *testing.T) {
	s, get := dashboardClient(t)
	s.ImportDir = t.TempDir()
	site := addSite(t, s, "example.com")
	h := s.Handler()
	c := sessionCookie(t, post(h, "/login", "password="+url.QueryEscape("correct horse")))
	base := "/sites/" + strconv.FormatInt(site.ID, 10)

	if w := get(base + "/import"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Nothing imported yet") {
		t.Fatalf("import page: %d", w.Code)
	}
	if w := get("/sites"); !strings.Contains(w.Body.String(), `href="`+base+`/import"`) {
		t.Error("sites page doesn't link the import page")
	}

	day := time.Now().AddDate(0, 0, -3).Format(dateLayout)
	w := upload(h, base+"/import", "plausible", plausibleExport(day), c)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Imported 1500 visits") {
		t.Fatalf("upload: %d: %s", w.Code, w.Body)
	}
	body := get("/?site=example.com").Body.String()
	if !strings.Contains(body, "/imported-page") || !strings.Contains(body, "1234") {
		t.Error("dashboard lacks the imported data")
	}
	filtered := get("/?site=example.com&f=" + url.QueryEscape("country|is|DE")).Body.String()
	if !strings.Contains(filtered, "Imported totals are left out") {
		t.Error("filtered dashboard doesn't say imported totals are left out")
	}
	if strings.Contains(body, "Imported totals are left out") {
		t.Error("unfiltered dashboard says imported totals are left out")
	}

	// The same days again, and broken files, are refused with a message.
	for name, c2 := range map[string]struct {
		format string
		file   []byte
		want   string
	}{
		"again":    {"plausible", plausibleExport(day), "already imported"},
		"not zip":  {"plausible", []byte("hello"), "not a ZIP file"},
		"umami":    {"umami", []byte("a,b\n"), "no created_at column"},
		"bad from": {"matomo", []byte("x"), "Choose where the data comes from"},
	} {
		w := upload(h, base+"/import", c2.format, c2.file, c)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), c2.want) {
			t.Errorf("%s: %d, want %q", name, w.Code, c2.want)
		}
	}

	// Cross-site uploads are rejected, and uploads need a login.
	r := httptest.NewRequest("POST", base+"/import", strings.NewReader(""))
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.AddCookie(c)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("cross-site upload: %d", w.Code)
	}
	if w := upload(h, base+"/import", "plausible", plausibleExport(day), nil); w.Code != http.StatusSeeOther {
		t.Errorf("anonymous upload: %d", w.Code)
	}

	ims, _ := s.Store.Imports(context.Background(), site.ID)
	if len(ims) != 1 {
		t.Fatalf("imports %v", ims)
	}
	if w := post(h, base+"/imports/"+strconv.FormatInt(ims[0].ID, 10)+"/delete", "", c); w.Code != http.StatusSeeOther {
		t.Errorf("delete: %d", w.Code)
	}
	if body := get("/?site=example.com").Body.String(); strings.Contains(body, "/imported-page") {
		t.Error("deleted import still shown")
	}
}
