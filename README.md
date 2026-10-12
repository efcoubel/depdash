# DepDash

English · [Français](README.fr.md)

[![CI](https://github.com/efcoubel/depdash/actions/workflows/ci.yml/badge.svg)](https://github.com/efcoubel/depdash/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

A Docker container that scans local projects and shows, in a web dashboard, how far each one is from the latest versions of its language, framework and dependencies.

DepDash is passive: the projects folder is mounted read-only, nothing is written to it, no PR is opened. Only public package names leave your machine, towards the registries.

The interface is in French for now.

## Getting started

With the published image, without cloning the repository:

```bash
docker run -d --name depdash -p 127.0.0.1:8080:8080 -v ~/projects:/projects:ro -v depdash-data:/data ghcr.io/efcoubel/depdash
```

The dashboard is at <http://localhost:8080>. A first scan starts at boot, then one runs every 6 hours.

With Docker Compose: copy `.env.example` to `.env`, point `PROJECTS_DIR` to the folder that holds your repos, then:

```bash
docker compose up -d
```

Without Docker, download a binary from the [releases](https://github.com/efcoubel/depdash/releases), or install it with Go:

```bash
go install github.com/efcoubel/depdash/cmd/depdash@latest
```

```bash
DEPDASH_PROJECTS_DIR=~/projects DEPDASH_DATA_DIR=./data depdash serve
```

| Command | Role |
| --- | --- |
| `depdash serve` | Dashboard and periodic scans (default) |
| `depdash scan` | One scan, then the inventory in the terminal; `-offline` does not query the registries |
| `depdash healthcheck` | Probes `/healthz`, used by the image's `HEALTHCHECK` |

## What is detected

| | PHP / Composer | Node / npm | Go |
| --- | --- | --- | --- |
| Manifest | `composer.json` | `package.json` | `go.mod` |
| Installed versions | `composer.lock` | `package-lock.json` (v1, v2, v3) | `go.mod` |
| Runtime, by priority | `.php-version`, `FROM php:X`, `config.platform.php`, `require.php` | `.nvmrc`, `.node-version`, `engines.node`, `FROM node:X` | `FROM golang:X`, `toolchain`, `go` |
| Framework | Symfony, Laravel | Next.js, Nuxt, Angular, React, Vue | — |

The dependency folder declared by `config.vendor-dir` in a `composer.json` is not walked. A manifest with no dependency, no lockfile and no runtime version is not kept as a project. For a Go module, a next major published under another path (`…/v4`) is not reported.

A repo is the folder that contains `.git`; it can hold several projects (monorepo). Without a lockfile, the manifest constraint is displayed and the status is estimated from it.

## Statuses

The status is not stored: it is recomputed on every page view.

| Status | Rule |
| --- | --- |
| Up to date | Latest version, or same minor as the latest |
| Minor update | Same major, minor behind |
| Major available | A newer major exists, current version still supported |
| End of support soon | Security support ends in less than 6 months (`DEPDASH_EOL_WARNING`) |
| Unsupported | Security support has ended |
| Unknown | Package not found or version not resolved |

For a runtime or a framework, an LTS that is still supported is "up to date" even if a newer major exists.

## Configuration

| Variable | Default | Role |
| --- | --- | --- |
| `DEPDASH_PROJECTS_DIR` | `/projects` | Scanned folder |
| `DEPDASH_DATA_DIR` | `/data` | SQLite database |
| `DEPDASH_PORT` | `8080` | HTTP port |
| `DEPDASH_SCAN_INTERVAL` | `6h` | Automatic scan frequency |
| `DEPDASH_MAX_DEPTH` | `4` | Manifest search depth |
| `DEPDASH_EOL_WARNING` | `180d` | "End of support soon" threshold |
| `DEPDASH_IGNORE` | — | Folders ignored on top of `vendor`, `node_modules`, `.git`, `var`, `dist`, `build`, `.next` (comma-separated) |
| `DEPDASH_MAPPING_FILE` | `$DEPDASH_DATA_DIR/mapping.json` | Package → endoflife.date product table |

To recognise another framework, copy [`internal/config/mapping.json`](internal/config/mapping.json) to `data/mapping.json` and add an entry. Without that file, the embedded table applies.

## Routes

| Method | Route | Role |
| --- | --- | --- |
| GET | `/` | Overview |
| GET | `/projects/{id}` | Project view |
| GET | `/packages/{ecosystem}/{name}` | Cross-project view of a package |
| POST | `/scan` | Global rescan |
| POST | `/projects/{id}/scan` | Rescan of one project |
| POST | `/projects/{id}/manual` | Manual entry |
| GET | `/api/projects` | JSON export |
| GET | `/healthz` | Healthcheck |

There is no authentication: `docker-compose.yml` publishes the port on `127.0.0.1` only, and POST requests coming from another site are refused.

## Data sources

| Data | Source | Cache |
| --- | --- | --- |
| Versions of a PHP package | `repo.packagist.org/p2/{vendor}/{package}.json` | 6 h |
| Versions of an npm package | `registry.npmjs.org/{package}` | 6 h |
| Latest version of a Go module | `proxy.golang.org/{module}/@latest` | 6 h |
| Lifecycles | `endoflife.date/api/{product}.json` | 24 h |

If an API is unavailable, the last cached value stays displayed with its date. Each registry receives at most 8 simultaneous requests, with a 10 s timeout and 2 attempts.

## Development

```bash
go test -race -cover ./...
```

To build the image from source with Docker Compose, add a `docker-compose.override.yml` (ignored by git):

```yaml
services:
  depdash:
    build: .
```

Pushing a `vX.Y.Z` tag publishes the image to GHCR and the binaries in a GitHub release.

```
cmd/depdash/         entry point, wiring
internal/config/     environment variables, mapping table
internal/scanner/    folder walk, project detection
internal/parser/     Parser interface; composer/, npm/, gomod/
internal/registry/   Registry and Lifecycle interfaces; packagist/, npm/, goproxy/, endoflife/
internal/status/     status computation
internal/store/      SQLite and migrations
internal/enrich/     cache filling from the registries
internal/scheduler/  periodic and on-demand scans
internal/web/        HTTP handlers, templates, embedded assets
testdata/            test manifests and lockfiles
```

Adding an ecosystem: write a `Parser` and a `Registry`, then declare them in `app()` of [`cmd/depdash/main.go`](cmd/depdash/main.go). The scanner, the enricher and the web interface do not need to change.

Code, comments and commit messages are written in French. Issues and pull requests are welcome in English or French.

## License

[MIT](LICENSE)
