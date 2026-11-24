# Importing from Plausible or Umami

Bring your history with you when you switch. Imports go into one site and
only cover the days **before** bonestats recorded its first visit to that
site, so no day is counted twice. Importing days that an earlier import
already covers is refused; delete that import first.

Data older than `BONESTATS_RETENTION_DAYS` (365 by default) is deleted,
imported or not. To keep a longer history, set it to `0` (keep forever) or a
larger number **before** you import.

There are two ways to import:

- **On the dashboard:** the site's settings → *Import*, choose the source
  and the file (up to 1 GB). Behind nginx, raise `client_max_body_size` for
  this; Caddy has no limit.
- **On the command line,** for any size:

  ```sh
  bonestats import -site example.com -from plausible plausible-export.zip
  bonestats import -site example.com -from umami umami.csv
  ```

  With Docker, copy the file into the data volume first:

  ```sh
  docker compose cp umami.csv bonestats:/data/umami.csv
  docker compose exec bonestats bonestats import -site example.com -from umami /data/umami.csv
  ```

  The command takes `-db` and `-tz` like the server (or `BONESTATS_DB` and
  `BONESTATS_TZ`); it can run while the server is running.

Every import is listed on the site's import page, where you can delete it
with all the data it brought in.

## Plausible

In Plausible, open the site's settings → *Imports & exports* → *Export*, and
download the ZIP it emails you. Import the ZIP as it is.

Plausible's export holds **daily totals** per page, source, country and so
on, not single visits. bonestats keeps them apart from its own data and adds
them to:

- the overview numbers and the chart,
- pages (with time on page and scroll depth), entry and exit pages,
- channels, sources, referrer URLs and UTM tags,
- countries, devices, browsers, operating systems and their versions,
- custom events and goals (pageview goals match imported pages).

Daily totals can't be filtered, so **while a filter is on, imported days are
left out**; the dashboard says so next to the filters. Not imported: regions
and cities (Plausible exports codes, bonestats shows names), custom
properties, revenue, funnels and Web Vitals. Unique visitors are counted per
day, so over a longer range someone who came on two days counts twice, as in
Plausible's own imported data.

## Umami

Umami's built-in export only holds top-500 lists without dates, so the import
reads its events straight from Umami's database instead. Imported this way,
Umami's visits become regular bonestats visits: every card, filter and
funnel works with them.

1. Find the website's ID in Umami: Settings → Websites → Edit, or the
   `website_id` in its tracking code.
2. Export its events from Umami's PostgreSQL database as CSV, replacing
   `YOUR-WEBSITE-ID`:

   ```sh
   psql "$DATABASE_URL" -c "COPY (
     SELECT e.created_at, e.session_id, e.visit_id, e.event_type, e.event_name,
            e.url_path, e.url_query, e.referrer_domain, e.referrer_path,
            s.browser, s.os, s.device, s.screen, s.country, s.city
     FROM website_event e JOIN session s ON s.session_id = e.session_id
     WHERE e.website_id = 'YOUR-WEBSITE-ID'
     ORDER BY e.session_id, e.created_at
   ) TO STDOUT WITH (FORMAT csv, HEADER)" > umami.csv
   ```

   With Umami's Docker Compose setup, run it in the database container:
   `docker compose exec -T db psql -U umami umami -c "COPY (…) TO STDOUT WITH (FORMAT csv, HEADER)" > umami.csv`.
   On an older Umami without `visit_id`, leave that column out; visits are
   then split after 30 minutes without an event. On MySQL, export the same
   columns to a CSV file with a header row.
3. Import `umami.csv`.

How the data is mapped:

- Pageviews (`event_type` 1) and custom events (`event_type` 2) are imported;
  link clicks and other event types are skipped. Event properties aren't
  imported.
- A visit's source, UTM tags, device, browser, OS, screen size and location
  come from its first event, and channels are worked out as for new visits.
- Umami doesn't measure how long a page was visible, so time on page is the
  time until the visit's next pageview, and the last page of a visit has
  none. Visit duration is then the time from the first to the last pageview,
  as in Umami.
- Visitors are counted per day, as bonestats does: an Umami session that
  spans several days counts once per day.
- Not imported: regions (Umami stores codes, bonestats shows names), browser
  versions (Umami has none) and Web Vitals.
