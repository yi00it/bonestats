# Stats API

A read-only JSON API for your statistics, for scripts, dashboards (Grafana,
Google Sheets…) and reports. It can't change anything.

## Keys

Create a key on the **Sites** page, under *Stats API*. The key is shown once,
right after you create it; bonestats stores only its hash. Revoke a key on the
same page. A key reads the statistics of every site.

Send the key in the `Authorization` header:

```sh
curl -H "Authorization: Bearer bs_…" "https://stats.example.com/api/v1/summary?site=example.com&range=30d"
```

## Parameters

Every endpoint except `/api/v1/sites` takes the same parameters as the
dashboard's URL, so you can copy them from the address bar:

| Parameter | |
|---|---|
| `site` | Required. The site's domain, e.g. `example.com`. |
| `range` | `today`, `yesterday`, `7d`, `28d`, `30d` (default), `91d`, `month`, `lastmonth`, `year`, `12mo`, `all` or `custom`. Days follow `BONESTATS_TZ`. |
| `from`, `to` | With `range=custom`: the first and last day, `YYYY-MM-DD`, at most 5 years apart. |
| `f` | A filter, `dimension\|operator\|value`, e.g. `f=country\|is\|DE`. Operators: `is`, `is_not`, `contains`. Repeat `f` for several filters. Dimensions are the ones listed under breakdown (except `event`), plus `goal` and `prop:<name>`. URL-encode the value (`\|` is `%7C`). |

Invalid parameters return `400` with `{"error": "…"}`, an unknown site `404`,
and a missing or wrong key `401`.

Every response echoes what it covers in `query`. `to` is the last day included:

```json
"query": {"site": "example.com", "from": "2026-10-25", "to": "2026-11-23", "filters": []}
```

## Endpoints

### `GET /api/v1/sites`

```json
{"sites": [{"domain": "example.com", "created_at": "2026-10-28T09:12:00Z"}]}
```

### `GET /api/v1/summary`

The dashboard's headline numbers. `compare=previous` or `compare=year` adds the
previous period or the same period a year earlier, up to the same point in
time, as the dashboard compares.

```json
{
  "query": {…},
  "results": {"visitors": 4088, "visits": 4088, "pageviews": 8553, "views_per_visit": 2.09, "bounce_rate": 44.99, "visit_duration": 91},
  "previous": {"from": "2026-09-25", "to": "2026-10-24", "results": {…}}
}
```

`bounce_rate` is a percentage, and `visit_duration` is the average visible
time per visit in seconds.

### `GET /api/v1/timeseries`

The same metrics per hour, day or month. `interval` is `hour`, `day` or
`month`; by default it follows the range like the dashboard's chart (hours up
to 2 days, days up to 92 days, months beyond). Hourly series cover at most 31
days.

```json
{"query": {…}, "interval": "day", "results": [{"date": "2026-11-22", "visitors": 151, "visits": 160, "pageviews": 334, "views_per_visit": 2.09, "bounce_rate": 43.75, "visit_duration": 88}, …]}
```

Days and months are `YYYY-MM-DD`; hours are RFC 3339 times in `BONESTATS_TZ`.

### `GET /api/v1/breakdown`

Top values of a dimension. `dimension` is one of `page`, `entry_page`,
`exit_page`, `channel`, `source`, `referrer`, `utm_source`, `utm_medium`,
`utm_campaign`, `utm_term`, `utm_content`, `country`, `region`, `city`,
`browser`, `browser_version`, `os`, `os_version`, `device`, `screen`, `event`
(custom events) or `prop:<name>` (a custom property's values). `limit` is 1 to
1000, default 100.

```json
{"query": {…}, "dimension": "page", "results": [{"value": "/", "visitors": 1855, "pageviews": 2028, "time_on_page": 42.51, "scroll_depth": 56.91}, …]}
```

Every row has `value` and `visitors`, plus a count named after what it counts:
`pageviews` (page), `entrances` (entry_page), `exits` (exit_page), `events`
(event and properties) or `visits` (everything else). Regions and cities add
their `country`; pages add `time_on_page` (seconds) and `scroll_depth`
(percent). With a `goal|is|…` filter, rows get a `conversion_rate`, and the
property values of a revenue goal get their `revenue`.

### `GET /api/v1/goals`

```json
{"query": {…}, "results": [{"goal": "purchase", "type": "event", "visitors": 61, "conversions": 64, "conversion_rate": 1.49, "revenue": 1913.6, "currency": "EUR"}, …]}
```

`revenue` and `currency` are `null` for goals without revenue.

### `GET /api/v1/funnels`

```json
{"query": {…}, "results": [{"name": "Signup flow", "steps": [{"goal": "Visit /pricing", "visitors": 1009, "percent": 100, "drop_off": 0}, …]}]}
```

### `GET /api/v1/vitals`

The 75th percentile of each Core Web Vital, overall and for the pages with the
most samples (`limit`, default 100). LCP, INP, FCP and TTFB are in
milliseconds; CLS has no unit. `p75` is `null` without samples.

```json
{"query": {…}, "results": {"lcp": {"p75": 1840, "samples": 912}, …}, "pages": [{"page": "/", "lcp": 1720, "inp": 96, "cls": 0.02, "fcp": 1100, "ttfb": 310, "samples": 402}, …]}
```

## CSV export

To download the dashboard's current view instead, use *Export this view as
CSV* at the bottom of the dashboard. It's a ZIP with one CSV file per card,
with the same columns as the API, for the range and filters you have
selected. Breakdowns are capped at 10,000 rows each.
