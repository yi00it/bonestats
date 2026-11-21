// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"io"
	"io/fs"
	"net/http/httptest"
	"strings"
	"testing"
)

// The privacy signal check is in bs.js by default and removed when the
// server ignores those signals; the opt-out flag stays either way.
func TestTrackerPrivacySignals(t *testing.T) {
	static, _ := fs.Sub(staticFS, "static")
	for _, ignore := range []bool{false, true} {
		h := trackerHandler(static, ignore)
		for _, file := range []string{"bs.js", "bs.outbound.downloads.js"} {
			r := httptest.NewRequest("GET", "/"+file, nil)
			r.SetPathValue("script", file)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			body, _ := io.ReadAll(w.Result().Body)
			js := string(body)
			if w.Code != 200 {
				t.Fatalf("%s: status %d", file, w.Code)
			}
			if got := strings.Contains(js, "globalPrivacyControl"); got == ignore {
				t.Errorf("ignore=%v %s: has privacy signal check = %v", ignore, file, got)
			}
			if !strings.Contains(js, "bonestats_ignore") || !strings.Contains(js, "n.webdriver") {
				t.Errorf("ignore=%v %s: opt-out or automation check missing", ignore, file)
			}
		}
	}
}
