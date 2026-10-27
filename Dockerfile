# SPDX-License-Identifier: AGPL-3.0-only

# bonestats is pure Go (no cgo), so every architecture is cross-compiled on
# the build machine instead of being emulated.
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/bonestats . \
 && mkdir -p /out/data

# The DB-IP Lite country database (CC BY 4.0, the dashboard shows the
# attribution), so locations work out of the box. A new month's file can be
# missing for the first hours of the month, so fall back to the previous one.
FROM --platform=$BUILDPLATFORM alpine:3.22 AS geoip
RUN apk add --no-cache curl
RUN set -eu; \
    y=$(date +%Y); m=$(date +%m); m=${m#0}; \
    if [ "$m" = 1 ]; then py=$((y - 1)); pm=12; else py=$y; pm=$((m - 1)); fi; \
    for month in "$y-$(printf %02d "$m")" "$py-$(printf %02d "$pm")"; do \
      if curl -fsSL --proto '=https' --tlsv1.2 -o /tmp/geo.mmdb.gz \
           "https://download.db-ip.com/free/dbip-country-lite-$month.mmdb.gz"; then \
        gunzip -t /tmp/geo.mmdb.gz && gunzip -c /tmp/geo.mmdb.gz > /dbip-country-lite.mmdb && exit 0; \
      fi; \
    done; \
    echo "could not download the DB-IP country database" >&2; exit 1

# distroless/static has CA certificates and tzdata, no shell, and runs as an
# unprivileged user.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/bonestats /usr/local/bin/bonestats
COPY --from=geoip /dbip-country-lite.mmdb /usr/share/bonestats/dbip-country-lite.mmdb
# Owned by the nonroot user, so a fresh named volume mounted here is writable.
COPY --from=build --chown=nonroot:nonroot /out/data /data
ENV BONESTATS_ADDR=:8080 \
    BONESTATS_DB=/data/bonestats.db \
    BONESTATS_GEOIP=/usr/share/bonestats/dbip-country-lite.mmdb
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/bonestats"]
