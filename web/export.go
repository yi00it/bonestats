// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// exportLimit caps the rows of each breakdown in the CSV export.
const exportLimit = 10000

// export serves the current dashboard view (site, range and filters) as a
// ZIP of CSV files, one per card.
func (s *Server) export(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st := s.store(r)
	v, err := s.resolveView(ctx, st, r.URL.Query())
	if errors.Is(err, errNoSites) {
		http.Redirect(w, r, "/sites", http.StatusSeeOther)
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	q := v.q

	type file struct {
		name string
		t    table
	}
	var files []file
	add := func(name string, t table, err error) error {
		if err == nil {
			files = append(files, file{name, t})
		}
		return err
	}
	sum, err := st.Summary(ctx, q)
	if err != nil {
		serverError(w, err)
		return
	}
	files = append(files, file{"summary.csv", summaryTable(sum)})
	iv := interval(v.from, v.to)
	t, err := timeseriesTable(ctx, st, q, iv)
	if err = add("timeseries.csv", t, err); err != nil {
		serverError(w, err)
		return
	}
	for _, dim := range breakdownDims() {
		t, err := breakdownTable(ctx, st, q, dim, exportLimit)
		if err = add(dim+".csv", t, err); err != nil {
			serverError(w, err)
			return
		}
	}
	steps := []struct {
		name string
		fn   func() (table, error)
	}{
		{"goals.csv", func() (table, error) { return goalsTable(ctx, st, q) }},
		{"properties.csv", func() (table, error) { return propertiesTable(ctx, st, q, exportLimit) }},
		{"funnels.csv", func() (table, error) { return funnelsTable(ctx, st, q, v.funnels) }},
		{"vitals.csv", func() (table, error) { return vitalsTable(ctx, st, q, exportLimit) }},
	}
	for _, step := range steps {
		t, err := step.fn()
		if err = add(step.name, t, err); err != nil {
			serverError(w, err)
			return
		}
	}

	// Built in memory, so an error can still become a 500 instead of a
	// broken download.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	modified := time.Now()
	for _, f := range files {
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: f.name, Method: zip.Deflate, Modified: modified})
		if err == nil {
			err = f.t.writeCSV(fw)
		}
		if err != nil {
			serverError(w, err)
			return
		}
	}
	if err := zw.Close(); err != nil {
		serverError(w, err)
		return
	}
	name := fmt.Sprintf("bonestats-%s-%s-%s.zip", v.site.Domain, v.from.Format(dateLayout), v.lastDay().Format(dateLayout))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(name))
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	w.Header().Set("Cache-Control", "no-store")
	w.Write(buf.Bytes())
}
