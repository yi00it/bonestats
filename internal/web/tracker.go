// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// Optional tracker add-ons, appended to bs.js when requested by file name,
// e.g. /bs.outbound.downloads.js. They only use the public bonestats() API.
var addons = []string{"downloads", "outbound"}

type script struct {
	plain, gz []byte
	etag      string
}

// trackerHandler serves bs.js and every add-on combination, each built and
// gzipped once at startup so visitors get the small version even without a
// compressing reverse proxy.
func trackerHandler(static fs.FS) http.Handler {
	core, err := fs.ReadFile(static, "bs.js")
	if err != nil {
		panic(err)
	}
	scripts := map[string]script{}
	for mask := 0; mask < 1<<len(addons); mask++ {
		body := append([]byte{}, core...)
		var names []string
		for i, a := range addons {
			if mask&(1<<i) != 0 {
				ext, err := fs.ReadFile(static, "ext/"+a+".js")
				if err != nil {
					panic(err)
				}
				body = append(body, ext...)
				names = append(names, a)
			}
		}
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		zw.Write(body)
		zw.Close()
		sum := sha256.Sum256(body)
		scripts[strings.Join(names, ".")] = script{body, buf.Bytes(), `"` + hex.EncodeToString(sum[:8]) + `"`}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sc, ok := scripts[addonKey(r.PathValue("script"))]
		if !ok {
			http.NotFound(w, r)
			return
		}
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Cache-Control", "public, max-age=3600")
		h.Set("Content-Type", "text/javascript; charset=utf-8")
		h.Set("Vary", "Accept-Encoding")
		h.Set("ETag", sc.etag)
		if r.Header.Get("If-None-Match") == sc.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		body := sc.plain
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			h.Set("Content-Encoding", "gzip")
			body = sc.gz
		}
		h.Set("Content-Length", strconv.Itoa(len(body)))
		w.Write(body)
	})
}

// addonKey turns "bs.outbound.downloads.js" into the canonical "downloads.outbound".
func addonKey(file string) string {
	name, ok1 := strings.CutPrefix(file, "bs.")
	name, ok2 := strings.CutSuffix(name, "js")
	if !ok1 || !ok2 {
		return "-"
	}
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == '.' })
	sort.Strings(parts)
	return strings.Join(parts, ".")
}
