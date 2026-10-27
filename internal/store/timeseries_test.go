package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
	_ "time/tzdata"
)

// Days must follow the local calendar on both sides of a DST change: Berlin
// switched from CEST (+2) to CET (+1) on 2026-10-25.
func TestTimeseriesAcrossDST(t *testing.T) {
	ctx := context.Background()
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.AddSite(ctx, "example.com")
	site, _ := s.SiteByDomain(ctx, "example.com")

	// 00:30 local, before and after the change.
	visits := []time.Time{
		time.Date(2026, 10, 20, 0, 30, 0, 0, berlin),
		time.Date(2026, 10, 27, 0, 30, 0, 0, berlin),
	}
	for i, at := range visits {
		if err := s.RecordEvent(ctx, Event{SiteID: site.ID, Time: at, Visitor: int64(i + 1), Name: "pageview", Path: "/"}); err != nil {
			t.Fatal(err)
		}
	}

	from := time.Date(2026, 10, 18, 0, 0, 0, 0, berlin)
	to := time.Date(2026, 10, 29, 0, 0, 0, 0, berlin)
	series, err := s.Timeseries(ctx, Query{SiteID: site.ID, From: from, To: to, Location: berlin}, Day)
	if err != nil {
		t.Fatal(err)
	}
	if len(series.Times) != 11 {
		t.Fatalf("got %d days, want 11", len(series.Times))
	}
	for i, ts := range series.Times {
		day := time.Unix(ts, 0).In(berlin)
		if day.Hour() != 0 || day.Minute() != 0 {
			t.Errorf("bucket %d starts at %v, want local midnight", i, day)
		}
		want := 0.0
		if day.Day() == 20 || day.Day() == 27 {
			want = 1
		}
		if got := series.Values["visits"][i]; got != want {
			t.Errorf("%s: visits = %v, want %v", day.Format("Jan 2"), got, want)
		}
	}

	// Months and hours keep working with a location too.
	for _, iv := range []Interval{Hour, Week, Month} {
		series, err := s.Timeseries(ctx, Query{SiteID: site.ID, From: from, To: to, Location: berlin}, iv)
		if err != nil {
			t.Fatal(err)
		}
		var n float64
		for _, v := range series.Values["pageviews"] {
			n += v
		}
		if n != 2 {
			t.Errorf("%s: pageviews = %v, want 2", iv, n)
		}
	}
}
