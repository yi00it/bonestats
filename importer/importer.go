// SPDX-License-Identifier: AGPL-3.0-only

// Package importer brings data from other analytics tools into a site:
// Plausible's CSV export (daily totals) and Umami's raw events (visits).
// See docs/IMPORT.md.
package importer

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/yi00it/bonestats/store"
)

const (
	Plausible = "plausible"
	Umami     = "umami"
)

// Formats lists the sources an import can come from.
var Formats = []string{Plausible, Umami}

// Result describes a finished import.
type Result struct {
	Import  store.Import
	Skipped int64 // rows from days bonestats already recorded itself
}

// ErrFormat matches errors in the imported file, which are the user's to
// fix; errors.As gives the *FormatError with the problem.
var ErrFormat = errors.New("invalid file")

type FormatError struct{ Problem string }

func (e *FormatError) Error() string        { return "invalid file: " + e.Problem }
func (e *FormatError) Is(target error) bool { return target == ErrFormat }

func formatError(format string, args ...any) error {
	return &FormatError{fmt.Sprintf(format, args...)}
}

// Run imports the file at path into site. Days on or after the site's first
// visit recorded by bonestats itself are skipped, so nothing is counted
// twice. loc is the dashboard's timezone. On failure nothing is kept.
func Run(ctx context.Context, st *store.Store, site store.Site, format, path string, loc *time.Location) (Result, error) {
	cutoff, ok, err := st.NativeStart(ctx, site.ID)
	if err != nil {
		return Result{}, err
	}
	if !ok {
		cutoff = time.Now().Add(24 * time.Hour)
	}
	f, err := os.Open(path)
	if err != nil {
		return Result{}, err
	}
	defer f.Close()

	w, err := st.BeginImport(ctx, site.ID, format)
	if err != nil {
		return Result{}, err
	}
	var skipped int64
	switch format {
	case Plausible:
		info, serr := f.Stat()
		if serr != nil {
			w.Abort()
			return Result{}, serr
		}
		zr, zerr := zip.NewReader(f, info.Size())
		if zerr != nil {
			w.Abort()
			return Result{}, formatError("not a ZIP file: use the ZIP from Plausible's export")
		}
		skipped, err = importPlausible(ctx, w, zr, loc, cutoff)
	case Umami:
		skipped, err = importUmami(ctx, w, f, site.Domain, loc, cutoff)
	default:
		err = fmt.Errorf("unknown import format %q", format)
	}
	if err != nil {
		w.Abort()
		return Result{}, err
	}
	im, err := w.Finish()
	if err != nil {
		return Result{}, err
	}
	if im.FirstDay == "" {
		if err := st.DeleteImport(context.WithoutCancel(ctx), site.ID, im.ID); err != nil {
			return Result{}, err
		}
		return Result{Skipped: skipped}, formatError("the file has no data from before %s, when bonestats started recording this site",
			cutoff.In(loc).Format("Jan 2, 2006"))
	}
	return Result{Import: im, Skipped: skipped}, nil
}

// openEntry opens a file in the ZIP, refusing absurd sizes.
func openEntry(f *zip.File) (io.ReadCloser, error) {
	if f.UncompressedSize64 > 4<<30 {
		return nil, formatError("%s is too large", f.Name)
	}
	return f.Open()
}
