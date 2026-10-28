// SPDX-License-Identifier: AGPL-3.0-only

package collect

import (
	"net/netip"

	"github.com/oschwald/maxminddb-golang/v2"
)

// Geo resolves an IP to a location using a local .mmdb file (DB-IP Lite
// country or city edition, or MaxMind). Lookups never leave the machine.
type Geo struct {
	db *maxminddb.Reader
}

// Location is what we keep: no coordinates, no postal codes.
type Location struct {
	Country, Region, City string
}

// OpenGeo opens the database at path. An empty path yields a Geo that always
// returns nothing, so location detection is optional.
func OpenGeo(path string) (*Geo, error) {
	if path == "" {
		return &Geo{}, nil
	}
	db, err := maxminddb.Open(path)
	if err != nil {
		return nil, err
	}
	return &Geo{db: db}, nil
}

func (g *Geo) Enabled() bool { return g.db != nil }

func (g *Geo) Lookup(ip netip.Addr) Location {
	if g.db == nil || !ip.IsValid() {
		return Location{}
	}
	var rec struct {
		Country struct {
			ISOCode string `maxminddb:"iso_code"`
		} `maxminddb:"country"`
		Subdivisions []struct {
			Names map[string]string `maxminddb:"names"`
		} `maxminddb:"subdivisions"`
		City struct {
			Names map[string]string `maxminddb:"names"`
		} `maxminddb:"city"`
	}
	if err := g.db.Lookup(ip).Decode(&rec); err != nil {
		return Location{}
	}
	loc := Location{Country: rec.Country.ISOCode, City: rec.City.Names["en"]}
	if len(rec.Subdivisions) > 0 {
		loc.Region = rec.Subdivisions[0].Names["en"]
	}
	return loc
}

func (g *Geo) Close() error {
	if g.db == nil {
		return nil
	}
	return g.db.Close()
}
