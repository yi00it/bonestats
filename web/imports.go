// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/yi00it/bonestats/importer"
	"github.com/yi00it/bonestats/store"
)

// maxUpload caps import uploads. Larger files can be imported with the
// bonestats import command.
const maxUpload = 1 << 30

func (s *Server) importsPage(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteFromPath(w, r)
	if !ok {
		return
	}
	s.renderImports(w, r, site, "", nil)
}

// renderImports shows the import page, with the outcome of an upload.
func (s *Server) renderImports(w http.ResponseWriter, r *http.Request, site store.Site, failure string, done *importer.Result) {
	ims, err := s.store(r).Imports(r.Context(), site.ID)
	if err != nil {
		serverError(w, err)
		return
	}
	if failure != "" {
		w.WriteHeader(http.StatusBadRequest)
	}
	s.render(w, "imports", map[string]any{
		"Title":     "Import · " + site.Domain,
		"Site":      site,
		"Imports":   ims,
		"Error":     failure,
		"Done":      done,
		"Retention": s.Retention,
	})
}

func (s *Server) uploadImport(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteFromPath(w, r)
	if !ok {
		return
	}
	// Large uploads and imports take longer than the server's timeouts
	// for ordinary requests.
	rc := http.NewResponseController(w)
	rc.SetReadDeadline(time.Now().Add(time.Hour))
	rc.SetWriteDeadline(time.Now().Add(2 * time.Hour))
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	mr, err := r.MultipartReader()
	if err != nil {
		s.renderImports(w, r, site, "Choose a file to import.", nil)
		return
	}
	// The format field comes before the file in the form.
	format, path := "", ""
	defer func() {
		if path != "" {
			os.Remove(path)
		}
	}()
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.renderImports(w, r, site, "The file is larger than 1 GB. Import it with the bonestats import command instead (see docs/IMPORT.md).", nil)
			return
		}
		if err != nil {
			s.renderImports(w, r, site, "The upload failed: "+err.Error(), nil)
			return
		}
		switch part.FormName() {
		case "format":
			b, _ := io.ReadAll(io.LimitReader(part, 32))
			format = string(b)
		case "file":
			if path != "" {
				continue
			}
			f, err := os.CreateTemp(s.ImportDir, "bonestats-import-*")
			if err != nil {
				serverError(w, err)
				return
			}
			path = f.Name()
			_, err = io.Copy(f, part)
			f.Close()
			if errors.As(err, &tooLarge) {
				s.renderImports(w, r, site, "The file is larger than 1 GB. Import it with the bonestats import command instead (see docs/IMPORT.md).", nil)
				return
			}
			if err != nil {
				s.renderImports(w, r, site, "The upload failed: "+err.Error(), nil)
				return
			}
		}
		part.Close()
	}
	if !contains(importer.Formats, format) {
		s.renderImports(w, r, site, "Choose where the data comes from.", nil)
		return
	}
	if path == "" {
		s.renderImports(w, r, site, "Choose a file to import.", nil)
		return
	}
	res, err := importer.Run(r.Context(), s.store(r), site, format, path, s.Location)
	var bad *importer.FormatError
	switch {
	case errors.As(err, &bad):
		s.renderImports(w, r, site, "Nothing was imported: "+bad.Problem+".", nil)
	case errors.Is(err, store.ErrImportOverlap):
		s.renderImports(w, r, site, "Nothing was imported: these days were already imported. Delete the earlier import first.", nil)
	case err != nil:
		log.Printf("import: %v", err)
		s.renderImports(w, r, site, "The import failed and nothing was kept. The server log has the details.", nil)
	default:
		s.renderImports(w, r, site, "", &res)
	}
}

func (s *Server) deleteImport(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteFromPath(w, r)
	if !ok {
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("iid"), 10, 64)
	if err := s.store(r).DeleteImport(r.Context(), site.ID, id); err != nil {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, "/sites/"+strconv.FormatInt(site.ID, 10)+"/import", http.StatusSeeOther)
}
