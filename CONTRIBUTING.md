# Contributing to bonestats

Bug reports, ideas and pull requests are welcome. For security problems,
follow [SECURITY.md](SECURITY.md) instead of opening an issue.

## Before you start

For anything larger than a small fix, open an issue first so we can agree on
the approach. bonestats keeps its footprint small on purpose: a single binary,
a single SQLite file and a tracking script of about 1.2 KB gzipped. A change
that adds dependencies or makes the tracking script bigger needs a strong
reason.

## Development

```sh
make build      # build ./bonestats
make test       # go test ./...
make demo       # add a demo site with 90 days of fake traffic
BONESTATS_PASSWORD='choose-a-long-password' make run
```

CI runs `gofmt`, `go vet`, the tests with the race detector and
`govulncheck`. Run them locally before you push:

```sh
gofmt -l . && go vet ./... && go test -race ./...
```

New Go files start with `// SPDX-License-Identifier: AGPL-3.0-only`.

## Contributor License Agreement

bonestats is released under the [AGPL-3.0](LICENSE). Before your first pull
request can be merged, you need to sign the [Contributor License
Agreement](CLA.md). You keep the copyright in your work. The agreement lets
the project relicense it or offer it under commercial terms (for example for
managed hosting), and it promises that everything included in a public
release stays available under that release's open-source license.

To sign it, reply to the CLA bot's comment on your pull request with the
sentence the bot asks for. You only need to do this once.
