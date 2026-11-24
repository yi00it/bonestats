<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/logo-dark.png">
    <img src="docs/logo-light.png" alt="bonestats" width="480">
  </picture>
</p>

<p align="center">
  A tiny, cookieless, self-hosted web analytics server.<br>
  One Go binary, one SQLite file, a 1.5 KB tracking script.
</p>

<p align="center">
  <a href="https://github.com/yi00it/bonestats/releases/latest"><img src="https://img.shields.io/github/v/release/yi00it/bonestats?sort=semver" alt="Latest release"></a>
  <a href="https://github.com/yi00it/bonestats/actions/workflows/ci.yml"><img src="https://github.com/yi00it/bonestats/actions/workflows/ci.yml/badge.svg?branch=master" alt="CI"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/yi00it/bonestats" alt="License: AGPL-3.0"></a>
  <a href="https://github.com/yi00it/bonestats/pkgs/container/bonestats"><img src="https://img.shields.io/badge/docker-ghcr.io-2496ED?logo=docker&logoColor=white" alt="Docker image"></a>
</p>

![The bonestats dashboard in light mode](docs/dashboard.png)

bonestats runs as a single Go binary with one SQLite file. Its tracking script is about 1.5 KB gzipped, smaller than Plausible's, and also measures Core Web Vitals, scroll depth and time on page. The dashboard looks like an instrument panel: dot-matrix display type, LED-style meters and a single signal-red accent, in a light and a dark theme.

## Install with Docker

On a server with Docker, a domain pointing at it, and ports 80 and 443 open:

```sh
mkdir bonestats && cd bonestats
curl -fsSLO https://raw.githubusercontent.com/yi00it/bonestats/master/docker-compose.yml
curl -fsSL -o .env https://raw.githubusercontent.com/yi00it/bonestats/master/.env.example
nano .env                                 # set BONESTATS_DOMAIN and BONESTATS_PASSWORD
docker compose up -d
```

This runs bonestats behind [Caddy](https://caddyserver.com), which gets an HTTPS certificate automatically, so open `https://<your domain>` and log in. The image (`ghcr.io/yi00it/bonestats`, amd64 and arm64) includes the DB-IP Lite country database, and the data lives in the `bonestats-data` volume. To update: `docker compose pull && docker compose up -d`. The compose network has IPv6 turned on so IPv6 visitors keep their real address; this needs Docker 27 or newer and IPv6 enabled in the host kernel. To back up (keep backups private, see below):

```sh
docker compose stop bonestats && docker compose cp bonestats:/data/bonestats.db ./bonestats-backup.db && docker compose start bonestats
```

Already running a reverse proxy? Run only the image and point your proxy at it (see [Production deployment](#production-deployment)):

```sh
docker run -d --name bonestats --restart unless-stopped -p 127.0.0.1:8080:8080 \
  -e BONESTATS_PASSWORD='choose-a-long-password' -e BONESTATS_PROXY_HEADER=X-Forwarded-For \
  -v bonestats-data:/data ghcr.io/yi00it/bonestats
```

Prebuilt binaries for Linux, macOS, Windows and FreeBSD are on the [releases page](https://github.com/yi00it/bonestats/releases).

## Build from source

```sh
make geoip                                # optional: countries (DB-IP Lite, 8 MB)
make geoip-city                           # optional: + regions and cities (130 MB)
make build
BONESTATS_PASSWORD='choose-a-long-password' make run
```

Open http://localhost:8080, log in, add your site (use `localhost` for local testing) and paste the snippet into your pages' `<head>`:

```html
<script defer data-domain="example.com" src="https://stats.example.com/bs.js"></script>
```

Custom events, with optional properties and revenue: `bonestats("purchase", {plan: "pro"}, 29.90)`. To queue events before the script loads, add
`<script>window.bonestats=window.bonestats||function(){(bonestats.q=bonestats.q||[]).push(arguments)}</script>`.

## Dashboard

![Pages, sources and locations](docs/breakdowns.png)

- **Metrics:** unique visitors, visits, pageviews, views per visit, bounce rate and visit duration. Each is compared with the previous period or the previous year, and you can click any of them to chart it.
- **Date ranges:** today, yesterday, 7/28/30/91 days, month to date, last month, year to date, 12 months, all time and custom.
- **Filters:** click any row (a page, source, country, browser…) to filter the whole dashboard, or use **+ Filter** for "is", "is not" and "contains". Filters live in the URL, so every view can be bookmarked.
- **Pages:** top pages (with time on page and scroll depth), entry pages and exit pages.
- **Sources:** channels (Organic Search, Social, Email, Paid, AI…), sources (well-known sites by name, so `google.com` and `google.com.tr` are both Google; visits without one are listed as Direct / None), referrer URLs (host and path, never the query string), and UTM source, medium, campaign, term and content.
- **Locations:** world map, countries, regions and cities.
- **Devices:** browser, browser version, OS, OS version, device type and screen size (all major versions and size ranges only).
- **Goals** (set them up in the site's settings → Goals & funnels): custom event goals, pageview goals with `*` wildcards, conversion rate and revenue. With a goal filter on, every breakdown shows a CR column.
- **Properties** of custom events, broken down by value (and revenue).
- **Funnels:** 2 to 8 goals that visitors must complete in order within one visit, with the drop-off at each step.
- **Web performance:** the 75th percentile of LCP, INP, CLS, FCP and TTFB from real visitors, rated good, needs improvement or poor, overall and per page. It follows the dashboard's filters.
- **Live:** the dashboard refreshes every 15 seconds while it's open and shows how many visitors are online. Numbers that changed flicker to their new value; open tabs and your scroll position are kept.
- **Export and API:** download the current view (range and filters included) as a ZIP of CSV files, or read the same numbers from scripts with the read-only [stats API](docs/API.md), using keys you create on the Settings page.
- **Import:** bring your history from Plausible (its export ZIP) or Umami (its raw events), on the site's *Import* page or with `bonestats import`. See [docs/IMPORT.md](docs/IMPORT.md).
- **Shared links:** give anyone a secret link to one site's dashboard (site settings → *Share*). They can pick any range and filter, without logging in and without seeing anything else. Give a link a password to ask for it first. Delete the link to turn it off.
- **Email reports:** weekly or monthly emails with a site's visitors, top pages, sources, countries and goals, compared with the period before (site settings → *Reports*). Needs an SMTP server, see `BONESTATS_SMTP_URL`. Every email has an unsubscribe link.
- **Light and dark:** follows your system setting, or pick one with the switch in the header (remembered in your browser).

![Funnel and web performance](docs/funnel-vitals.png)

To leave out your own visits, open the browser console on your site and run `localStorage.bonestats_ignore = "true"`, once per browser and device (`localStorage.removeItem("bonestats_ignore")` undoes it). The script only reads this flag and never writes anything to visitors' devices.

Add-ons are opt-in via the script name: `bs.outbound.js` tracks clicks to other sites, `bs.downloads.js` tracks file downloads, and `bs.downloads.outbound.js` does both.

## Demo data

`make demo` (or `./bonestats demo -replace`) adds a site called `demo.example.com` with 90 days of realistic fake traffic, plus goals and funnels, so you can explore the dashboard before connecting a real site. Options: `-site`, `-days`, `-visitors` (average per day), `-db`, and `-replace` (recreate the site if it already exists). To remove the data, delete the site from its settings (*General* → *Delete site*).

## Configuration

| Env var / flag | Default | |
|---|---|---|
| `BONESTATS_PASSWORD` | (required) | Admin password, at least 12 characters. Changing it logs out every session. After 10 wrong attempts from one IP (or 100 from all IPs together), logins are blocked for 15 minutes. |
| `BONESTATS_PASSWORD_FILE` / `-password-file` | (off) | Read the password from this file instead (e.g. a Docker or systemd secret). |
| `BONESTATS_ADDR` / `-addr` | `:8080` | Listen address |
| `BONESTATS_DB` / `-db` | `bonestats.db` | SQLite file |
| `BONESTATS_GEOIP` / `-geoip` | (off) | Path to a country `.mmdb` (DB-IP Lite or MaxMind) |
| `BONESTATS_RETENTION_DAYS` / `-retention` | `365` | Delete data older than N days (`0` = keep forever) |
| `BONESTATS_TZ` / `-tz` | `Local` | Timezone for dashboard days, e.g. `Europe/Berlin` |
| `BONESTATS_PROXY_HEADER` / `-proxy-header` | (off) | Behind a reverse proxy: the header it sets to the client's IP, e.g. `X-Forwarded-For` (the last entry is used, the one the proxy appended) or `X-Real-IP`. Only that header is read. See [Production deployment](#production-deployment). |
| `BONESTATS_TRUST_PROXY` / `-trust-proxy` | `false` | Deprecated, same as `BONESTATS_PROXY_HEADER=X-Forwarded-For`. |
| `BONESTATS_ALLOW_HTTP` / `-allow-http` | `false` | Allow logging in over plain HTTP from hosts other than `localhost`. Only for a trusted private network: the password and login cookie are sent unencrypted. |
| `BONESTATS_EVENT_RATE` / `-event-rate` | `120` | Beacons per minute accepted from one IP (IPv6: one /64), with bursts of the same size. More are dropped, so one client can't flood the database. `0` = unlimited. |
| `BONESTATS_IGNORE_PRIVACY_SIGNALS` / `-ignore-privacy-signals` | `false` | Count visitors who send Global Privacy Control or Do Not Track like everyone else. By default they are skipped. See [Privacy design](#privacy-design). |
| `BONESTATS_SMTP_URL` / `-smtp-url` | (off) | SMTP server for email reports: `smtp://user:password@smtp.example.com:587` (STARTTLS) or `smtps://user:password@smtp.example.com:465` (TLS from the start). Percent-encode `@`, `:` and `/` in the user name or password. The password is only sent over TLS, or to localhost. |
| `BONESTATS_SMTP_FROM` / `-smtp-from` | (off) | From address of email reports, e.g. `bonestats <stats@example.com>`. Your SMTP provider has to allow sending from it. |
| `BONESTATS_SOURCE_URL` / `-source-url` | `https://github.com/yi00it/bonestats` | Linked as "Source code" in the dashboard footer. If you run a modified version, point it at your source: the AGPL asks you to offer it to your users. |

## Production deployment

bonestats speaks plain HTTP. On the internet, put it behind a reverse proxy that terminates HTTPS, and tell bonestats which header carries the client's IP. Without that, every visitor seems to come from the proxy: visitors are miscounted and anyone can lock you out of the login for 15 minutes (bonestats logs a warning when it notices this).

Caddy (sets `X-Forwarded-For` and `X-Forwarded-Proto` by default):

```
stats.example.com {
	reverse_proxy 127.0.0.1:8080
}
```

nginx, overwriting whatever the client sent:

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-Proto $scheme;
}
```

Then start bonestats with `BONESTATS_PROXY_HEADER=X-Forwarded-For` (Caddy) or `BONESTATS_PROXY_HEADER=X-Real-IP` (nginx above), and `BONESTATS_ADDR=127.0.0.1:8080` so it can't be reached around the proxy. Name only a header your proxy always overwrites: any other header comes straight from the client and can be forged. With a chain of proxies, e.g. a CDN in front of nginx, make the last one set the real client IP.

The admin login only works over HTTPS (or on `localhost`), and the cookie is then `__Host-` prefixed and `Secure`. Every response carries `X-Robots-Tag: noindex, nofollow`, so search engines keep your stats domain out of their results. Don't also add a `Disallow: /` robots.txt: crawlers would then never see the header. The database file is created readable by its owner only; keep it that way and keep backups private too, as it holds the current day's salt.

## What is collected

Per visit: source (the referrer's host or name) and referrer path (no query string), channel, UTM source/medium/campaign/term/content, browser and OS family plus major version, device class (from the user agent), screen-size range (from viewport width), country/region/city, and a visitor hash. The script also reports whether the URL carries an ad click ID (`gclid`, `msclkid` and the like; only its name, never its value) and how many touch points the screen has, to tell an iPad from a Mac; both are used to classify the visit and not stored.
Per pageview: path (no query string, percent-decoded so `/yaz%C4%B1l%C4%B1m` reads `/yazılım`), scroll depth and visible time. Path segments that look like personal data or secrets are replaced before storing: email addresses become `[email]`, and UUIDs, long hex IDs and mixed-case tokens (as in `/reset/<token>`) become `[id]`, in page paths and referrer paths alike. Readable slugs such as `/iPhone-17-Pro-review` are kept. This is a best-effort guard, so still avoid personal data in your URLs.
Per custom event: name, up to 10 short properties, and an optional revenue amount.
Per page load: Core Web Vitals (LCP, INP, CLS, FCP, TTFB). A visit ends after 30 minutes without a beacon. Visit duration is the time the visit's pages were actually visible, so a tab left in the background doesn't add to it; a bounce is a visit with one pageview.

## Privacy design

- **No cookies or storage** on visitors' devices, and no fingerprinting.
- **IP addresses are never stored.** They are used in memory for the country lookup (done locally, nothing leaves the server) and for the visitor hash.
- Visitor hash = `sha256(daily_salt + domain + IP + User-Agent)`; of an IPv6 address only the /64 network is used, since devices keep changing the rest. The salt is random and **deleted from the database within minutes after midnight** (in `BONESTATS_TZ`), overwritten on disk, so hashes can't be reversed or linked across days. The running server keeps it in memory for another 30 minutes, only so that a visit in progress at midnight isn't counted as a second visitor; a restart drops it. As a result, someone who returns on another day counts as a new visitor. While a day's salt exists, someone holding the database could try to brute-force that day's hashes back to IP addresses, which is why the file is private.
- **Global Privacy Control and Do Not Track are honored** in the script and again on the server, unless you set `BONESTATS_IGNORE_PRIVACY_SIGNALS=true`. Since nothing is stored on the device, no IP or lasting ID is kept and no data is sold or shared, you may decide that anonymous counting doesn't need these signals; Plausible and Fathom don't skip such visitors either. If you do, say so in your privacy policy.
- Bots and automated browsers are filtered out. Known bot user agents are dropped on arrival. Bots that pose as a normal browser usually load a page and are gone before the browser reports any time on it, so a visit only shows on the dashboard once one of its pages has been visible for at least a second. The rest are kept in the database but never counted. A side effect: real visitors who leave within a second aren't counted either, and someone who is still reading appears in the live count only after their first interaction or when they leave the page, and drops out of it after five minutes without scrolling, clicking or typing.
- Old data is deleted automatically (see retention).

This is the same approach as Plausible and Fathom, which commonly run without a consent banner under GDPR/ePrivacy. It isn't legal advice: mention the analytics in your privacy policy, and check with a lawyer for your situation. Host the server in the EU to avoid transfer questions.

The DB-IP Lite data is licensed CC BY 4.0. The dashboard shows the required attribution when GeoIP is enabled.

## The tracking script

`web/static/bs.js` is hand-minified to keep it tiny (about 2.6 KB raw, 1.5 KB gzipped). The add-ons in `static/ext/` are appended to it when requested by file name. The server builds and gzips every combination at startup and serves them with an ETag. Here's what the script does:

1. Stops immediately (and makes `bonestats()` do nothing) if the visitor sends GPC or DNT (unless the server ignores them), if it's an automated browser, or if `localStorage.bonestats_ignore` is `"true"`.
2. Sends a `pageview` beacon on load, on SPA navigations (`history.pushState`, `history.replaceState` and `popstate`, when the path changes) and when the browser brings the page back from its back/forward cache. A page the browser prerenders sends nothing until it is actually shown. The external referrer is only sent with the first one.
3. `bonestats(name, props, revenue)` sends a custom event, and calls queued before the script loaded are replayed.
4. It tracks the deepest scroll position, reported as a share of the page's height at the time of each report, and the time the page was visible.
5. It watches LCP, FCP, CLS (session windows), INP (about the 98th percentile of interactions) and TTFB with `PerformanceObserver`.
6. When the page is hidden or unloaded, or before an SPA navigation, it sends a `leave` beacon with the page's scroll depth and visible time. Until the first SPA navigation it also carries the vitals measured so far; the server keeps one set per page load.
7. On the visitor's first real click, tap, key press or mouse-wheel scroll after the page has been visible for a second, it sends one early `leave` beacon (without vitals). Mobile browsers often drop the beacon sent as the page closes, and this one makes sure the visit still counts.
8. While the page stays visible and the visitor keeps scrolling, clicking or typing, it sends further `leave` beacons (without vitals) after 15 seconds, then at doubling intervals up to every 4 minutes. A long read is then measured even if the last beacon is lost, and keeps its visit open.

Beacon fields: `d` domain, `n` name, `p` path, `w` viewport width, `t` touch points, `r` referrer, `c` name of an ad click ID in the URL, `source`/`medium`/`campaign`/`term`/`content` UTM tags, `pr` properties, `rv` revenue, `sd` scroll depth, `et` visible ms, `v` vitals. Beacons are sent with `navigator.sendBeacon` as plain text, which avoids CORS preflight requests.

## Layout

```
main.go                    config, startup, retention and report jobs
store                      SQLite schema and migrations, writes, filters, dashboard queries, channels
collect                    /api/event: validation, hashing, UA parsing, GeoIP
importer                   imports from Plausible and Umami
report                     email reports: content, schedule, SMTP
web                        dashboard, goals pages, login, templates, static files (bs.js + add-ons, jsvectormap, fonts)
demo                       demo data generator
docs                       README images
```

## License

bonestats is Copyright (c) 2026 yi00it and released under the [GNU Affero General Public License v3.0](LICENSE) (AGPL-3.0-only). You can use, modify and self-host it freely; if you run a modified version as a network service, you must make your modified source available to its users. Versions released before the switch to AGPL remain available under the MIT License. To contribute, see [CONTRIBUTING.md](CONTRIBUTING.md). The bundled [jsvectormap](https://github.com/themustafaomar/jsvectormap) (including its world map) is MIT-licensed too; its license is in `web/static/LICENSE.jsvectormap.txt`. The bundled [Geist](https://github.com/vercel/geist-font) and [Doto](https://github.com/oliverlalan/Doto) fonts are under the SIL Open Font License 1.1 (`web/static/fonts/OFL-*.txt`). The DB-IP Lite data you download is CC BY 4.0.
