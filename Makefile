.PHONY: build run test geoip geoip-city demo

build:
	go build -ldflags="-s -w" -o bonestats .

# Uses the city database when downloaded, else the country one, else none.
GEOIP := $(firstword $(wildcard data/dbip-city-lite.mmdb data/dbip-country-lite.mmdb))

run: build
	./bonestats -geoip "$(GEOIP)"

test:
	go test ./...

# Download this month's free DB-IP Lite country database (CC BY 4.0).
geoip:
	mkdir -p data
	curl -fL "https://download.db-ip.com/free/dbip-country-lite-$$(date +%Y-%m).mmdb.gz" | gunzip > data/dbip-country-lite.mmdb

# City edition (~60 MB download, ~130 MB on disk): adds regions and cities.
geoip-city:
	mkdir -p data
	curl -fL "https://download.db-ip.com/free/dbip-city-lite-$$(date +%Y-%m).mmdb.gz" | gunzip > data/dbip-city-lite.mmdb

# Add a demo site with 90 days of fake traffic (delete it on the Sites page to remove it).
demo: build
	./bonestats demo -replace
