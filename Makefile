.PHONY: build run test geoip geoip-city demo

build:
	go build -ldflags="-s -w" -o bonestats .

# Uses the city database when downloaded, else the country one, else none.
GEOIP := $(firstword $(wildcard data/dbip-city-lite.mmdb data/dbip-country-lite.mmdb))

run: build
	./bonestats -geoip "$(GEOIP)"

test:
	go test ./...

# download fetches a DB-IP Lite database over HTTPS only, checks that the
# archive is intact and only then replaces the old file, so a failed or
# truncated download never leaves a broken database behind.
define download
	mkdir -p data
	curl -fsSL --proto '=https' --tlsv1.2 -o data/$(1).mmdb.gz.tmp "https://download.db-ip.com/free/$(1)-$$(date +%Y-%m).mmdb.gz"
	gunzip -t data/$(1).mmdb.gz.tmp
	gunzip -c data/$(1).mmdb.gz.tmp > data/$(1).mmdb.tmp
	rm data/$(1).mmdb.gz.tmp
	mv data/$(1).mmdb.tmp data/$(1).mmdb
endef

# Download this month's free DB-IP Lite country database (CC BY 4.0).
geoip:
	$(call download,dbip-country-lite)

# City edition (~60 MB download, ~130 MB on disk): adds regions and cities.
geoip-city:
	$(call download,dbip-city-lite)

# Add a demo site with 90 days of fake traffic (delete it on the Sites page to remove it).
demo: build
	./bonestats demo -replace
