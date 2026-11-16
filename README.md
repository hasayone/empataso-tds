**English** · [Русский](README.ru.md)

# EmTDS

[![CI](https://github.com/egerkuzma/emtds/actions/workflows/ci.yml/badge.svg)](https://github.com/egerkuzma/emtds/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/egerkuzma/emtds)](https://goreportcard.com/report/github.com/egerkuzma/emtds)
[![Go version](https://img.shields.io/github/go-mod/go-version/egerkuzma/emtds)](go.mod)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

**A fast, secure, self-hosted Traffic Distribution System (TDS) written in Go.**

EmTDS takes an incoming visitor, decides **by rules** where to send them
(redirect / iframe / JavaScript / inline content / stub), tells **bots apart from
humans**, and records everything for analytics — a single long-running engine,
an admin panel embedded in its own binary, and a small background service that
keeps the lists and geo databases fresh.

It is built for throughput and safety: IP lists and signatures live in memory
(`O(log n)` lookups), logs and conversions go to **ClickHouse**, counters and
sessions go to **Redis**, and the response never waits for a log write.

![A flow in the admin panel: visitors fall down the trunk, each stream branches off to its destination](docs/img/flow.png)

---

## Table of contents

- [Why EmTDS](#why-emtds)
- [Features](#features)
- [Screenshots](#screenshots)
- [Architecture](#architecture)
- [Performance](#performance)
- [Benchmark vs zTDS](#benchmark-vs-ztds)
- [Quick start](#quick-start)
- [Install on Ubuntu](#install-on-ubuntu)
- [Configuration](#configuration)
- [Core concepts](#core-concepts)
- [Filters and flag semantics](#filters-and-flag-semantics)
- [Output: redirect types & macros](#output-redirect-types--macros)
- [Bot detection](#bot-detection)
- [Postback & API mode](#postback--api-mode)
- [Admin web interface](#admin-web-interface)
- [HTTP endpoints](#http-endpoints)
- [Testing](#testing)
- [Project layout](#project-layout)
- [Security](#security)
- [Roadmap](#roadmap)
- [License](#license)

---

## Why EmTDS

A TDS sits in front of your offers and landings and routes every click to the
right place based on who the visitor is. **EmTDS does it like infrastructure —
not like a script.**

- ⚡ **Blazing fast** — **~30,000 req/s** through the *full* pipeline on a single
  laptop-class M1, p50 ~1 ms, **0 errors**. The core IP lookup is **~9 ns** with
  zero allocations.
- 📈 **Scales instead of melting** — flat throughput and zero errors from 25 to
  400 concurrent connections. In a head-to-head benchmark it is **~135× faster**
  than a classic PHP TDS — which *collapses* under the very same load.
- 🧠 **Everything in memory** — IP lists, group config, signatures and geo live in
  one long-running process; logs are written **asynchronously** to ClickHouse, so
  the hot path never blocks on disk.
- 🛡️ **Secure by default** — argon2id passwords, server-side sessions, CSRF,
  parameterized queries, `X-Forwarded-For` and `CF-IPCountry` believed only
  from trusted proxies, JSON-only input.
- 🎛️ **Batteries included** — a polished, **embedded** admin panel: flows drawn
  as they work (drag streams to reorder, line widths follow the traffic),
  conditions and outputs shown as words and rows instead of `|`-separated
  strings, a visitor test that shows which rule stopped whom, dashboard, logs,
  conversions, `.dat` editor, automation. No Node build, no separate web server.
- 🕒 **A background service** — `cmd/cron` keeps bot IP lists and geo databases
  fresh, checks your domains with VirusTotal, watches the disk and reports
  conversions to Telegram.
- 🧩 **One binary, optional deps** — ClickHouse and Redis are optional; the engine
  runs without them and simply skips the corresponding features.
- ✅ **Genuinely tested** — unit tests + **23 end-to-end scenarios** + ClickHouse
  integration + an embedded-JS syntax check, all green in CI.
- 🆓 **Open source (MIT)** — self-hosted and fully yours to run, modify and ship.

> **The short version:** a modern, in-memory, single-binary TDS that stays fast
> and predictable exactly where older PHP-based systems fall over.

---

## Features

**Traffic segmentation (per stream):**
- Geo: country / city / region / **time zone** (MaxMind-format `.mmdb`, or the
  CDN's `CF-IPCountry` for the country).
- Network: **ASN** and **organization** (from an ASN `.mmdb`) — e.g. keep
  hosting networks away from an offer.
- Device: computer / phone / tablet.
- OS, browser (+ versions), device brand (Apple, Samsung, Xiaomi, …).
- WAP carrier (operator) by IP ranges from `wap.dat`.
- Language, referer, domain, keyword (`?q=`), any **URL parameter**
  (`utm_source=fb`), and arbitrary IP lists.
- Yandex.Browser, uniqueness, referer presence.
- Weekly schedule (per day of week).
- Per-stream impression limits (per day / per rolling window).

**Bot handling:**
- UA/referer signatures, search-engine IP lists (Google/Bing/Yandex/Yahoo/Mail/
  Baidu/Others), empty UA/referer/language, IPv6, reverse DNS (PTR), UA blacklist.
- Bots are detected **after** stream selection (per the chosen stream's toggles).
- Optional separate output for bots (`bot_redirect`), or `skip` to serve them the
  normal stream while still logging them.
- `save_ip`: append a detected search-engine IP back into its list.

**Output & rendering:**
- 18 redirect types (HTTP redirect, JS, meta refresh, iframe, inline HTML, error
  pages, JSON API responses, stub, …) — including **`group`: hand the visitor
  to another group**, so flows can be chained.
- A rich macro set: `[KEY] [IP] [COUNTRY] [CITY] [REGION] [LANG] [DEVICE]
  [OPERATOR] [DOMAIN] [USERAGENT] [CID] [ASN] [ORG] [TIMEZONE] [UTC]
  [PAR-1..5] [()COUNTRY()] [()CITY()]
  [RANDNUM-a-b] [RANDSTR-(set)-n] [RANDLINE-(file)-n] [RANDDFL-(dir)-n]`.
- Output-variant distribution with `|||`: `random` / `rotator` (cookie) /
  `evenly` (Redis counter).
- `chance` (show with probability), `separation` (swap output by keyword from a
  `.dat`), `[REMOTE]` (fetch external content with caching), CURL redirect
  (fetch + find/replace), `api_mac` (mac code in API responses).

**Operations:**
- Group config, IP lists, separation lists, bot signatures and the geo
  databases are re-read in the background when their files change — admin
  edits and fresh downloads go live without restarting the engine.
- The **cron service** (`cmd/cron`): bot IP lists from URLs (incl. the JSON
  Google and Bing publish), MaxMind-format database downloads, VirusTotal
  checks of the domains streams send to, free-disk alerts, keyword-file
  cleanup, Telegram notifications for all of it and for new conversions.
- A malformed or missing config is skipped and retried, leaving the running
  rules in place.

**Uniqueness & protection:**
- Uniqueness by IP (Redis) or by cookie (dedicated cookie, correct TTL).
- Anti-flood (max requests per IP per window).
- Login rate-limit (Redis, fixed window per IP).

**Analytics & admin:**
- **Flows**: every group drawn as a trunk with a branch per stream, widths
  following the traffic; drag to reorder; edit in a drawer; send a stream on
  to another flow; **test a visitor** and see which condition stopped them.
- Dashboard: KPI tiles (visits, unique, bots, conversions, profit, CR), a time
  chart, a **performance table by flow and stream** (hits / unique / bots /
  conversions / profit / CR, sortable, CSV) and breakdowns (country/device/OS/
  browser/source/network).
- Logs with multi-select filters loaded from real data, IP search, CSV export,
  country flags, and a detail view per visit (network, User-Agent, referer).
- Conversions (postbacks), collected keywords, `.dat` list editor, an IP
  lookup tool, the automation page, password change. Light and dark theme.

---

## Screenshots

All taken from a running stack with generated traffic.

**A flow** — the group as it works. Visitors enter at the top and fall down the
trunk; each stream is a branch to its destination; what matches nothing ends in
the default. Line widths and the labels on them are the traffic of the selected
period. Drag a stream by its handle to change the order:

![Flow](docs/img/flow.png)

**Test a visitor** — give an IP and a device and see the way through the flow:
the matched stream in green, and on every other one the condition that turned
the visitor away. It runs on the flows as edited, before saving:

![Test a visitor](docs/img/flow-test.png)

**Stream editor** (dark theme) — a drawer over the page. *When*: only the
conditions this stream uses, values as chips, more from "Add condition". *Then*:
where to send — several variants are rows — including another flow. Bots and the
rare options are folded below:

![Stream editor](docs/img/stream.png)

**Flows** — all of them, with the period's numbers and where they link to:

![Flows](docs/img/flows.png)

**Dashboard** — what works: visits, conversions and profit, the performance
table by flow and stream, and who the visitors are:

![Dashboard](docs/img/dashboard.png)

**Logs** — multi-select filters (values loaded from the data for the selected
period), CSV export; a row opens the visit's detail:

![Logs](docs/img/logs.png)

**Tools** — what the engine learns from an IP address: location, network, the
lists it is in, and the geo databases in use:

![Tools](docs/img/tools.png)

**Automation** — the cron service: what runs, when it last ran and what it said:

![Automation](docs/img/automation.png)

---

## Architecture

Four services and two tools, shared `internal/` packages:

| Binary | Port | Purpose |
|--------|------|---------|
| `cmd/engine` | `:8080` | The hot path — traffic handling. A long-running process. |
| `cmd/admin` | `:8090` | REST API + embedded admin SPA (`internal/admin/web`, `go:embed`). |
| `cmd/cron` | — | Background jobs: list and geo database updates, VirusTotal, disk, Telegram. Talks to the others through files only. |
| `cmd/apiclient` | `:9090` | Client to install on a landing/donor page (calls the engine via `?api=`). |
| `cmd/mcp` | — | MCP server over stdio: gives an AI agent the admin API through an API key ([docs/wiki/mcp.md](docs/wiki/mcp.md)). Built with `make mcp`. |
| `cmd/loadgen` | — | Click generator for test campaigns ([docs/wiki/loadgen.md](docs/wiki/loadgen.md)). |

**Request lifecycle in the engine:**

```
HTTP request
  │
  ├─ postback?  (?pb=KEY&cid=&profit=) → record conversion, exit
  ├─ realip middleware  (trust XFF/CF only from trusted_proxies)
  ├─ api mode?  (?api=base64(JSON), checks EMTDS_API_KEY)
  ├─ IP blacklist        → 403 if listed
  ├─ resolve group by id/alias (first path segment); none → "trash" mode
  ├─ anti-flood (Redis): N requests / IP / window
  ├─ detect device/OS/browser/brand ; geo + ASN + time zone (mmdb ;
  │  CF-IPCountry only via a trusted proxy) ; carrier (wap)
  ├─ uniqueness: cookie | Redis SETNX
  ├─ router.Select(group, visitor)  → first stream that passes all filters
  │  └─ type "group" → the same visitor through another group (max 3 hops)
  ├─ bot detection by the SELECTED stream's toggles → bot_redirect (or skip)
  ├─ separation · fetch [REMOTE] · chance · distribution (|||) · api_mac
  ├─ render: macros + redirect type (CURL = fetch+find/replace; api = JSON);
  │  the [REMOTE] body is spliced in AFTER macro expansion, never rescanned
  ├─ collect keywords (save_keys / keys_se)
  └─ async batch log → ClickHouse  (the response never waits for the write)
```

Key principles: **state lives in process memory** (IP indexes, config,
signatures, geo) and is refreshed in the background; **rules are data** (a list of
predicates evaluated in a loop); indexes are swapped **atomically** on hot-reload.
Full details: [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md).

---

## Performance

Measured on a **base Apple M1** (4 performance + 4 efficiency cores, 8 GB RAM),
macOS 15, Go 1.26 — with the **engine, ClickHouse, Redis, and the load generator
all running on the same laptop**. They compete for the same 8 cores, so these are
deliberately conservative "everything on one machine" numbers; a dedicated load
host and separate database hosts would push them higher.

The load generator sends requests with varied client IPs (`X-Forwarded-For`), a
mobile User-Agent, and `CF-IPCountry: RU`; redirects are **not** followed, so the
engine's own response is what's measured. Every scenario runs the **full request
pipeline** (real-IP resolution → blacklist → geo → device/OS/browser/brand detect
→ uniqueness → stream routing → bot toggles → macro rendering). Logging to
ClickHouse is asynchronous and never blocks the response.

![Performance benchmark on Apple M1](docs/img/performance.png)

| Scenario | Requests/sec | p50 | p99 | Errors |
|---|--:|--:|--:|--:|
| **Full pipeline** · cookie uniqueness · async log → ClickHouse (302) | **~30,000** | ~1 ms | ~11 ms | 0 |
| Uniqueness by IP · Redis `SETNX` on every request (200) | ~16,000 | ~2.6 ms | ~10 ms | 0 |
| Active bot detection · search-engine IP lists + signatures (200) | ~13,000 | ~3 ms | ~16 ms | 0 |

Throughput stays flat (~30k req/s) and **error-free** as concurrency rises from 25
to 400 — latency grows but nothing drops. The hot path is allocation-light: the
core CIDR/IP-range lookup runs in **~9 ns/op with 0 allocations**
(`go test -bench=Lookup ./internal/ipindex` → ~130M lookups/sec on a single core).

> Throughput differences between rows come purely from how much work each path
> adds (a Redis round-trip per request, or several IP-list lookups for bots). On
> server-class hardware with the databases on separate hosts, expect materially
> higher numbers.

---

## Benchmark vs zTDS

To put those numbers in context, we ran EmTDS **head-to-head against zTDS** — a
popular legacy PHP TDS — on the **same Apple M1**, through the **same request
pipeline** (geo, search-engine IP-list checks, macro rendering, per-request
logging) and the **same load generator**.

![EmTDS vs zTDS — head-to-head benchmark](docs/img/vs-ztds.png)

| | EmTDS (Go) | zTDS (PHP) |
|---|--:|--:|
| Single thread | **11,352 req/s** · p50 80 µs | 84 req/s · p50 ~10 ms |
| 8 concurrent | **23,626 req/s** · 0 errors | collapses (errors, multi-second tails) |
| 50 concurrent | **27,800 req/s** · 0 errors | collapses (mostly errors) |
| Scaling | flat to 400 concurrent, **0 errors** | degrades from 2 concurrent, fails by 8 |

**EmTDS wins decisively** — about **135× faster on a single thread**, and under
real concurrent load the gap becomes effectively unbounded: the PHP TDS simply
**stops serving**, while EmTDS keeps scaling without a single error.

Why such a blowout? It's **architecture, not micro-optimisation**. EmTDS keeps
**everything in memory** — IP indexes, group config, signatures, geo — inside one
long-running process, and writes logs **asynchronously** to ClickHouse, so the hot
path never blocks. Classic PHP TDS spin up a fresh process on every request, read
files from disk on every hit, and write each log line **synchronously into a
single SQLite file behind a global write-lock** — which serializes all traffic and
collapses the moment real concurrency arrives.

> **In short: everything in memory + async logging = a TDS that scales instead of
> melting under load.** This is exactly where EmTDS is built to stay fast and
> predictable while older PHP-based systems fall over.

Methodology and caveats — see [Performance](#performance) above. These are
conservative "all on one laptop" numbers; on server hardware both the absolute
throughput and the gap grow further.

---

## Quick start

### Prerequisites
- Go (1.25+), Docker (for ClickHouse + Redis).

```bash
brew install go docker
```

### 1. Clone & start infrastructure
```bash
git clone https://github.com/egerkuzma/emtds.git emtds
cd emtds
make infra-up          # ClickHouse + Redis via docker compose; schema auto-applied
```

### 2. Generate an admin password hash
```bash
go run ./cmd/admin -hash 'my-strong-password'   # prints $argon2id$...
```

### 3. Run the engine (`:8080`)
```bash
EMTDS_DATA_DIR=./data \
EMTDS_GROUPS_FILE=configs/groups.example.json \
EMTDS_TRUSTED_PROXIES=127.0.0.1/32 \
EMTDS_POSTBACK_KEY=secret EMTDS_API_KEY=apikey \
EMTDS_REDIS_ADDR=localhost:6379 \
EMTDS_CLICKHOUSE_ADDR=localhost:9000 EMTDS_CLICKHOUSE_DB=kuztds \
EMTDS_CLICKHOUSE_USER=kuztds EMTDS_CLICKHOUSE_PASSWORD=devpassword \
go run ./cmd/engine
```

### 4. Run the admin panel (`:8090`)
```bash
EMTDS_ADMIN_PASSWORD_HASH='<hash-from-step-2>' \
EMTDS_ADMIN_PASSWORD_FILE=./admin.hash \
EMTDS_ADMIN_COOKIE_SECURE=false \
EMTDS_ENGINE_URL=http://localhost:8080 \
EMTDS_GROUPS_FILE=configs/groups.example.json \
EMTDS_DATA_DIR=./data EMTDS_KEYS_DIR=./keys \
EMTDS_REDIS_ADDR=localhost:6379 \
EMTDS_CLICKHOUSE_ADDR=localhost:9000 EMTDS_CLICKHOUSE_DB=kuztds \
EMTDS_CLICKHOUSE_USER=kuztds EMTDS_CLICKHOUSE_PASSWORD=devpassword \
go run ./cmd/admin
```

Open **http://localhost:8090** and log in with `admin` and your password.

### 5. Run the cron service (optional)
```bash
EMTDS_CRON_FILE=./cron.json \
EMTDS_DATA_DIR=./data EMTDS_KEYS_DIR=./keys \
EMTDS_GROUPS_FILE=configs/groups.example.json \
EMTDS_GEO_DB=./geo/city.mmdb EMTDS_ASN_DB=./geo/asn.mmdb \
EMTDS_CLICKHOUSE_ADDR=localhost:9000 EMTDS_CLICKHOUSE_PASSWORD=devpassword \
go run ./cmd/cron
```
Give the engine and the admin the same `EMTDS_GEO_DB` / `EMTDS_ASN_DB` (the
DB-IP ASN file `dbip-asn.mmdb` goes next to them, or set `EMTDS_DBIP_ASN_DB`), and the
admin the same `EMTDS_CRON_FILE`; then everything it does is configured on the
**Automation** page. Nothing runs until a job is switched on there.

### 6. Send a test request to the engine
```bash
curl -i 'http://localhost:8080/promo?q=hello' \
  -H 'X-Forwarded-For: 8.8.8.8' \
  -H 'User-Agent: Mozilla/5.0 (iPhone; CPU iPhone OS 16_0 like Mac OS X) Safari/604.1' \
  -H 'CF-IPCountry: US'
```
Diagnostic response headers show the decision: `X-Emtds-Stream`, `X-Emtds-Bot`,
`X-Emtds-Country`, `X-Emtds-Device`, `X-Emtds-Uniq`.

> A `Makefile` is provided: `make build`, `make test`, `make bench`,
> `make infra-up`, `make infra-down`.

---

## Install on Ubuntu

The `deploy/install.sh` script performs a one-time install of EmTDS on a clean
Ubuntu 22.04 or 24.04 host: Docker (if missing), ClickHouse, Redis, the engine,
the admin panel, cron, and php. The local [Quick start](#quick-start) path for
development stays the same.

| | Minimum — the installer exits below this | For long-term use |
|---|------------------------------------------|-------------------|
| RAM | 8 GB | 16 GB |
| CPU | 2 cores | 4 cores |
| Free disk (`/var/lib/docker` or `/var`) | 40 GB | 80 GB, 200 GB NVMe preferred |

Between the minimum and the recommendation the installer warns and continues;
clicks and conversions are kept for a year, so disk sizing follows your daily
click volume.

In `.env`, the ClickHouse password (`CLICKHOUSE_PASSWORD`) must not be empty,
and the admin password (`EMTDS_ADMIN_PASSWORD`) must not be empty or `admin`.
Run the script as a file with `bash`, not as `curl | bash`.

```bash
git clone https://github.com/egerkuzma/emtds.git emtds
cd emtds
cp .env.example .env
nano .env        # CLICKHOUSE_PASSWORD and EMTDS_ADMIN_PASSWORD
sudo bash deploy/install.sh
```

The installer:

- checks Ubuntu, root, memory, CPU cores, and disk space;
- installs Docker from download.docker.com only when `docker compose` is not
  available yet, and does not change an already working Docker setup;
- builds the `emtds:local` image (the state volume is `emtds-app_emtds_state`, mounted at `/var/lib/emtds`);
- starts ClickHouse and Redis with the `deploy` compose project, then
  `deploy/docker-compose.app.yml`;
- picks memory limits from the host RAM and writes them to
  `/etc/emtds/compose.infra.yml` and `/etc/emtds/compose.app.yml`;
- binds ClickHouse, Redis, the admin panel, and the engine to `127.0.0.1`
  only (via `/etc/emtds/server.env`). On the host the engine is
  `127.0.0.1:18080`; inside the container it still listens on `:8080`;
- treats the install as successful only after the engine and admin logs show
  `clickhouse connected` and `redis connected`, and `/healthz` and
  `/api/health` respond;
- when nginx is installed, runs `deploy/apply-edge.sh`: the `emtds*.conf` files
  from `deploy/nginx` (backup, `nginx -t`, rollback) and the keys
  `EMTDS_PUBLIC_IP` / `EMTDS_TRUSTED_PROXIES` in `/etc/emtds/server.env`. They
  use the self-signed IP certificate `/etc/emtds/tls/ip.crt` and `ip.key`; the
  scripts do not create it. Without nginx the stack is installed without the edge;
- enables the `emtds-watchdog.timer` watchdog ([docs/wiki/watchdog.md](docs/wiki/watchdog.md)).

On success it writes the marker `/etc/emtds/installed` and deletes
`deploy/install.sh`. A second run while the marker exists does nothing.
`git checkout` can restore the script file, but the marker blocks another run.
If the install failed, there is no marker and the script remains: fix the error
from the message and run again. Containers are not stopped on failure.

### Restart

```bash
sudo /etc/emtds/up.sh
```

This brings the same stack back with the same limits after a reboot or a manual stop.

Do not run `make infra-up` or bare `docker compose` on the server without the files under `/etc/emtds`: ClickHouse and Redis would be recreated without memory limits and with ports on all interfaces, and Redis would lose in-memory data.

### Updating

After the first install, the update agent is set up automatically (`deploy/update.sh --install`). It checks for a new commit or release on a schedule and shows it in **Profile → Updates**.

Updates are applied manually only — via the button or CLI:

```bash
# Apply the latest version for the configured channel:
sudo /usr/local/lib/emtds/update.sh --channel

# Apply a specific tag:
sudo /usr/local/lib/emtds/update.sh --to v1.2.3
```

Full reference — channels, signing, rollback, manual steps, error codes: [docs/wiki/updates.md](docs/wiki/updates.md).

### Good to know

- The admin port `8090` is bound to `127.0.0.1`. With nginx on the host the panel
  is served at `https://<server IP>/` (self-signed certificate) and, for a domain
  with the primary or admin role, at `https://admin.<domain>/` through Cloudflare.
  Without nginx log in with `ssh -L 8090:127.0.0.1:8090 user@server`, then open
  http://localhost:8090. Do not expose port 8090 to the internet: the container
  sets the cookie without `Secure`, and ufw rules do not block Docker-published
  ports.
- On the server the engine is at `127.0.0.1:18080`. Host port `8080` stays free
  for other programs. An external Nginx can proxy to `127.0.0.1:18080`; the
  address inside the container does not change.
- The admin password from `.env` applies only on a fresh volume. After you
  change the password in the UI, the volume's `admin.hash` takes over.
- The ClickHouse schema from `migrations/clickhouse` is applied only on an
  empty volume. On an existing volume the installer does not migrate and stops if
  the `events` table lacks a `step` column.
- Domains and their DNS are configured in the panel (Cloudflare and Namecheap
  integrations, [docs/wiki/namecheap.md](docs/wiki/namecheap.md)); nginx files
  on the server are changed only through `deploy/apply-edge.sh`.
- Do not use `docker compose down` or `make infra-down` on the server.

---

## Configuration

Everything is configured via `EMTDS_*` environment variables; **secrets are never
written to files in the repo**. Full reference: [`docs/USAGE.md`](docs/USAGE.md).

### Engine (most-used)
| Variable | Purpose |
|----------|---------|
| `EMTDS_LISTEN` | listen address (default `:8080`) |
| `EMTDS_DATA_DIR` | directory of `.dat` lists (IP, wap, signatures, separation) |
| `EMTDS_GROUPS_FILE` | JSON groups config (source of truth) |
| `EMTDS_TRUSTED_PROXIES` | trusted proxy CIDRs for `XFF`/`CF` (comma-separated) |
| `EMTDS_GEO_DB` | path to a MaxMind-format City or Country `.mmdb` (optional; without it the country comes from `CF-IPCountry`) |
| `EMTDS_ASN_DB` | path to a MaxMind-format ASN `.mmdb` (optional; enables the ASN / organization filters) |
| `EMTDS_DBIP_ASN_DB` | path to the DB-IP ASN Lite `.mmdb` (optional; without it the file `dbip-asn.mmdb` next to `EMTDS_ASN_DB`, else next to `EMTDS_GEO_DB`). DB-IP wins over MaxMind for ASN and organization, MaxMind answers on a miss and its spelling is kept as `OrgAlt`/`ASNAlt` for the filters. Attribution: "IP to ASN data by DB-IP.com", CC BY 4.0 — [wiki/geo-databases.md](docs/wiki/geo-databases.md) |
| `EMTDS_RELOAD_INTERVAL` | how often changed files are re-read (default `1m`) |
| `EMTDS_REDIS_ADDR` / `_PASSWORD` | Redis (uniq/limit/firewall) |
| `EMTDS_CLICKHOUSE_ADDR` / `_DB` / `_USER` / `_PASSWORD` | ClickHouse (logs) |
| `EMTDS_POSTBACK_KEY` | key for the `?pb=` postback |
| `EMTDS_API_KEY` | key for the `?api=` client mode |
| `EMTDS_KEYS_DIR` | directory for collected keywords |
| `EMTDS_KEYS_MAX_BYTES` | cap of one daily keyword file in bytes, default 8388608 (8 MiB); `0` disables it |
| `EMTDS_TRASH_MODE` / `_URL` | behaviour for an unknown group (0=200,1=redirect,2=403,3=404) |
| `EMTDS_INSTANCE` | engine instance name, `[a-z0-9-]` up to 32 characters (default `engine`). An invalid name turns the metrics file off and is logged as an error |
| `EMTDS_METRICS_DIR` | directory of the metrics file `<EMTDS_INSTANCE>.json`, refreshed every 10 s and on shutdown (default `/var/lib/emtds/metrics`); the same data is at `GET /internal/metrics`, closed to the public in nginx |
| `EMTDS_SPOOL_DIR` | root of the on-disk event queue (default `/var/lib/emtds-spool`, volume `emtds_spool`, mounted by the engine only). Batches that cannot be written to ClickHouse wait in `<root>/<EMTDS_INSTANCE>/events/` and are written after it recovers. When the variable is set and the directory cannot be opened the engine does not start; a second process with the same `EMTDS_INSTANCE` is refused |
| `EMTDS_SPOOL_MAX_BYTES` | ceiling of the whole spool root in bytes (default 2147483648); beyond it events are lost with the cause `spool_full` |
| `EMTDS_SPOOL_MIN_FREE_PCT` | percent of the spool volume that must stay free, 0 to 90 (default `10`, `0` means no floor); below it an event is lost with the cause `spool_full` |
| `EMTDS_SPOOL_VOLUME` | name of the Docker volume that holds the spool (compose only, default `emtds-app_emtds_spool`). It is not part of the `update.sh` backup and `down -v` does not remove it; delete it only when the spool is empty. If `EMTDS_SPOOL_DIR` is not a mount point the engine starts and logs a warning: the queue would be lost when the container is recreated |

### Shutdown budget

On `SIGTERM` the engine drains in two sequential phases, ten seconds each:
`http.Server.Shutdown` for in-flight requests, then the log buffer for events
that have not reached ClickHouse yet. **Give the process at least 25 seconds to
stop.**

The budgets are separate on purpose: sharing one deadline meant a busy server
could spend all of it and hand the log drain a context that was already dead.

Defaults are shorter than that almost everywhere — Docker stops a container
10 seconds after `SIGTERM`, and systemd's `TimeoutStopSec` is often lower than
you think. A `SIGKILL` in the middle of the drain costs the last batch of events
*and* the `engine stopped` line that reports what was lost.

```yaml
services:
  engine:
    stop_grace_period: 30s
```

```ini
[Service]
TimeoutStopSec=30
```

### Admin (most-used)
| Variable | Purpose |
|----------|---------|
| `EMTDS_ADMIN_LISTEN` | address (default `:8090`) |
| `EMTDS_ADMIN_USER` | login (default `admin`) |
| `EMTDS_ADMIN_PASSWORD_HASH` | argon2id hash |
| `EMTDS_ADMIN_PASSWORD_FILE` | hash file (takes priority; UI password changes are written here) |
| `EMTDS_ENGINE_URL` | engine base URL (for group links in the UI) |
| `EMTDS_GROUPS_FILE` / `EMTDS_DATA_DIR` / `EMTDS_KEYS_DIR` | same paths as the engine |
| `EMTDS_GEO_DB` / `EMTDS_ASN_DB` / `EMTDS_DBIP_ASN_DB` | same paths as the engine (IP lookup, visitor test) |
| `EMTDS_CRON_FILE` | the cron service's config file (enables the Automation page) |

### Cron service
| Variable | Purpose |
|----------|---------|
| `EMTDS_CRON_FILE` | its config (JSON, written by the admin; default `cron.json`). State is kept next to it in `<file>.status.json` |
| `EMTDS_DATA_DIR` / `EMTDS_KEYS_DIR` / `EMTDS_GROUPS_FILE` | same paths as the engine |
| `EMTDS_GEO_DB` / `EMTDS_ASN_DB` / `EMTDS_DBIP_ASN_DB` | where downloaded databases are put — the config chooses what to download, never where to write |
| `EMTDS_CLICKHOUSE_*` | only for conversion notifications |
| `EMTDS_METRICS_DIR` | where the engines write their metrics files; cron reads them for the click event loss alert (Telegram and the admin banner) |

### Groups config (JSON)
A group is reachable at `/<id>` (and at each alias). Example shape:

```json
[
  {
    "id": "promo",
    "name": "Promo (RU/US segmentation)",
    "status": true,
    "redirect": "show_text",
    "out": "FALLBACK",
    "uniq_method": "cookie",
    "uniq_seconds": 86400,
    "firewall": { "enabled": false, "queries": 100, "seconds": 60 },
    "save_keys": true,
    "aliases": ["ru"],
    "streams": [
      {
        "name": "ru_mobile",
        "status": true,
        "rules": { "country": { "flag": 2, "values": ["ru"] }, "computer": 1, "tablet": 1 },
        "out": { "redirect": "http_redirect", "out": "https://m.example.com/?k=[KEY]&c=[COUNTRY]" },
        "bots": { "ch_ua": true, "ch_empty_ua": true, "ch_bot_ip_google": true, "redirect": "404_not_found" }
      },
      {
        "name": "de_to_offers",
        "status": true,
        "comment": "German traffic goes on to the offers group",
        "rules": { "country": { "flag": 2, "values": ["de"] } },
        "out": { "redirect": "group", "out": "offers" }
      },
      { "name": "default", "status": true, "out": { "redirect": "show_text", "out": "no offer" } }
    ]
  }
]
```

`de_to_offers` hands German visitors to the group whose id is `offers` — another
entry of the same file (see *Group links* below).

You normally **edit groups in the admin UI** ("Flows" → open a flow → click a
stream → "Save"). The engine notices the file changed and swaps the config in within
`EMTDS_RELOAD_INTERVAL` (1 min by default) — no restart. A malformed or missing
file leaves the running config untouched. See `configs/groups.example.json` and
`configs/test_groups.json`.

---

## Core concepts

- **Group** — a routing target reachable at `/<id>`; the admin panel calls it
  a **flow**. It holds defaults (`redirect`/`out`/`header`),
  uniqueness/firewall settings, and a list of **streams**.
- **Stream** — a set of **rules** (predicates) plus an **output**. The router
  picks the **first active stream that passes all of its rules**; order matters.
- **Rules are data** — each filter is a value with a flag; the router evaluates
  them in a loop. If no stream matches, the group defaults / `trash` mode apply.
- **Group links** — a stream (or a group's default) of type `group` hands the
  visitor to another group, whose streams are then tried with the same visitor.
  Chains are limited to three hops; a link to a missing or disabled group
  answers like an unknown group.
- **Trash mode** — what to return for an unknown/disabled group: `200` empty,
  redirect, `403`, or `404`.

---

## Filters and flag semantics

Most list filters use a three-state flag (the zero value is "off", so unset
filters never block traffic):

| Flag | Meaning (list filters) | Meaning (device/operator) |
|:----:|------------------------|---------------------------|
| `0` | **off** — filter not applied | — |
| `1` | **exclude** — reject on match (blacklist) | block this device/operator |
| `2` | **include** — reject on absence of a match (whitelist) | require (whitelist) |

In the UI these read **is / is not**; a condition that is not on the stream is
off. `lang`/`country` use "contains" semantics; `city`/`region`/`brand` use
exact match; `ua`/`referer`/`key`/`org` accept `/regex/` or a substring;
`os`/`browser` match over `"name version"`; `asn` takes numbers (`AS15169` or
`15169`); `timezone` takes UTC offsets (`+3`, `+5:30`) or zone names
(`Europe/Moscow`); `get` takes `name` (present) or `name=value`.

---

## Output: redirect types & macros

**Redirect types** (`out.redirect`):

| Type | Result |
|------|--------|
| `http_redirect` | `302` with `Location: <out>` |
| `meta_refresh`, `js_redirect`, `iframe_redirect`, `iframe_selection`, `js_selection` | HTML that navigates the browser |
| `javascript` | `200` JS body |
| `show_text` | `200` plain text |
| `show_page_html` | `200` HTML page wrapping `<out>` |
| `under_construction` | `200` stub page |
| `stop` | `200`, empty body |
| `403_forbidden`, `404_not_found`, `500_server_error` | error pages |
| `api` | `200` JSON `{out,type,country,device,…}` (for api clients) |
| `show_out` | `200` JSON `{out,type:1,mac}` |
| `curl` | fetch a URL server-side, apply find/replace, return the body |
| `group` | hand the visitor to the group named in `out` |

**Macros** (expanded in `out`):

`[KEY]` (url-encoded keyword) · `[PATH]` (host) · `[IP]` · `[COUNTRY]` `[CITY]`
`[REGION]` · `[ASN]` `[ORG]` (url-encoded) · `[TIMEZONE]` `[UTC]` · `[LANG]` ·
`[DEVICE]` · `[OPERATOR]` · `[DOMAIN]` · `[USERAGENT]` ·
`[CID]` (click id, for postbacks) · `[PAR-1..5]` (extra GET params) ·
`[()COUNTRY()]` `[()CITY()]` · `[RANDNUM-a-b]` · `[RANDSTR-(charset)-n]` ·
`[RANDLINE-(file)-n[/u]]` · `[RANDDFL-(dir)-n[/u]]`.

**Distribution** — put several variants in `out` separated by `|||` (in the
admin: **Add a variant** under the output) and choose `distribution`: `random`,
`rotator` (each visitor gets the next one; the position is kept in a cookie), or
`evenly` (Redis counter, round-robin for everyone).

---

## Bot detection

Per stream, toggle which checks apply (section **Bots** of the stream editor): UA/referer
signatures, empty UA/referer/language, IPv6, PTR (reverse DNS), UA blacklist, and
search-engine IP lists. Detection runs **after** stream selection. Then:

- `redirect: "skip"` (or empty) — serve the normal stream output, but still log
  the visitor as a bot.
- `redirect: "404_not_found"` (or any redirect type) + optional `out`/`header` —
  serve bots a **separate** output (`bot_redirect`).
- `save_ip: true` — append a detected search-engine IP back into its `.dat` list.

---

## Postback & API mode

**Postback (conversion tracking).** Put a click id into your offer URL via the
`[CID]` macro, then have the offer call back:

```
http://your-host/?pb=YOUR_POSTBACK_KEY&cid=[CID]&profit=1.50
```
The engine finds the original event by `cid` and records the conversion (visible
under **Conversions**).

**API mode.** A landing page can ask the engine for a decision without redirecting
the visitor itself: send `?api=base64(JSON)` (authenticated with `EMTDS_API_KEY`)
containing the visitor data; the engine returns a JSON decision. `cmd/apiclient`
is a ready-made client that collects visitor data, calls the engine, and applies
the result (redirect or content).

---

## Admin web interface

A single embedded page (light and dark theme; English, Russian and Ukrainian), no build step. One
**top bar** carries the navigation, the period picker, the theme toggle and the
user menu; there are no sidebars. Every page has an address (`#/flows/promo`),
so back/forward and links work. Whatever can be edited is compared with what
was last saved, and a bar at the bottom offers **Save** / **Discard** on any
page — drafts survive moving around the panel.

- **Dashboard** — six KPI tiles (visits, unique, bots, conversions, profit, CR),
  a time chart with hover values, the **performance table** by flow and stream
  (sortable, CSV, click to open the flow), breakdowns by country, device, OS,
  browser, source and network.
- **Flows** — cards for every group with the period's numbers. A flow's page
  draws it as it works: the entry, a **trunk** the visitors fall down, a
  **branch** per stream to its destination, the default at the bottom; widths
  and labels are the traffic of the period. Drag a stream by its handle to
  reorder, flip its switch, duplicate it or move it to another flow from its
  menu. Click a stream to edit it in a **drawer**: *When* (only the conditions
  it carries; 23 kinds behind "Add condition" — geo, time zone, ASN,
  organization, operator, IP list, device, brand, OS, browser, language,
  referer, domain, keyword, URL parameter, User-Agent, unique, days of week,
  impression limit), *Then* (type, output, variants — or another flow), *Bots*,
  *Advanced*, and a note shown on the canvas.
- **Test a visitor** (on a flow's page) — IP, device, country, language,
  referer, keyword, URL parameters → the path through the flow, the matched
  stream, and for every other stream the condition that stopped the visitor.
  Works on unsaved edits and follows links to other flows.
- **Logs** — multi-select filters whose values are loaded from the data of the
  period, IP field, humans/bots, CSV export; a row opens the visit's detail.
- **Conversions** — postbacks and total profit. **Keywords** — collected per
  flow and day. **Lists** — editor for `.dat` files.
- **Tools** — what the engine learns from an IP: location, network, which
  lists it is in, and the state of the geo databases.
- **Automation** — the cron service: a card per job with its switch, interval,
  settings, last result and "Run now"; Telegram with a test message.
- **Profile** (user menu) — password, company name and logo, API keys for
  scripts and AI agents, users, boards. **Integrations** — Keitaro import,
  Cloudflare, Namecheap.
- **Catalogs and access** — offers, landings, affiliate networks, traffic
  sources, domains, the report, bot protection, HTML pages, roles. All of it is
  described in [docs/wiki/README.md](docs/wiki/README.md).

---

## HTTP endpoints

**Engine:**
- `GET /<id>` — serve a group (optionally `?q=<keyword>`, `?p1=..&p2=..`).
- `GET /?pb=KEY&cid=..&profit=..` — postback pixel.
- `GET /?api=base64(JSON)` — api-client mode.
- `GET /healthz` — health probe. The body stays `ok`; the number of log events
  that never reached ClickHouse rides along in headers, so it can be scraped
  without a metrics endpoint: `X-Events-Lost: <total>` and
  `X-Events-Lost-Detail: full=<n> queue=<n> insert=<n> late=<n>` (the intake
  channel was full / the batch queue was full / the insert failed / the event
  arrived during shutdown). The same counters are logged on exit.
- `GET|POST /postback` — keyless conversion path ([docs/wiki/postbacks.md](docs/wiki/postbacks.md)).
- `GET /l/<id>/…` — assets of a local landing.

**Admin API** (open without a session: `GET /api/health`, `GET /healthz`,
`GET|POST /api/setup`, `POST /api/login`, `POST /api/recover`, `GET /api/brand`;
the rest needs a session cookie with `X-CSRF-Token` for writes, or an
`Api-Key` header):
`POST /api/logout`, `GET /api/me`, `POST /api/password`,
`GET /api/stats/{summary,timeseries,breakdown,performance,facts}`,
`GET /api/logs`, `GET /api/logs/filters`, `GET /api/logs/export`,
`DELETE /api/logs`, `GET /api/postbacks`, `GET /api/report`, `GET /api/keys`,
`GET|PUT /api/groups`, `PUT|DELETE /api/groups/{id}`,
CRUD of `/api/offers`, `/api/landings`, `/api/traffic-sources`,
`/api/affiliate-networks`, `/api/domains`, `GET /api/lists`,
`GET|PUT /api/lists/{name}`, `GET /api/lookup`, `POST /api/simulate`,
`GET|PUT /api/cron`, `POST /api/cron/run`, `GET|POST|PUT|DELETE /api/api-keys…`,
`GET /api/openapi.json` and more. The full map with sections, cookie-only
routes and dangerous actions is in [docs/wiki/admin-api.md](docs/wiki/admin-api.md).

---

## Testing

```bash
go test ./...                              # unit tests (22 packages)
go test -tags=integration ./...            # + ClickHouse/Redis round-trips (needs make infra-up)
go test -tags=uitest ./internal/admin/     # the embedded SPA: its JS parses, its value conversions hold (needs node)
go vet ./...
make bench                                 # ipindex (~10 ns/lookup) and geo lookup benchmarks
```

Highlights: `cmd/engine/e2e_test.go` drives **23 end-to-end scenarios** through
the full pipeline (all redirect types, all macros, bots, geo, filters, operators,
distribution, limits, firewall, separation, schedule, chance, api mode, and a
traffic matrix). `internal/cron` runs every job against local HTTP servers —
downloads in all formats, refusals of bad ones, alerts, the schedule.
`internal/admin/web_logic_test.go` runs the admin's value conversions under
node — word lists ↔ regular expressions, flags, output variants — so what the
editor shows is saved back exactly. ClickHouse tests are behind the `integration` build tag and skip
automatically when ClickHouse is unavailable. Coverage snapshot:
[`docs/STATUS.md`](docs/STATUS.md).

---

## Project layout

```
cmd/
  engine/       hot path (traffic handling)
  admin/        REST API + embedded SPA
  cron/         background service
  apiclient/    landing/donor client
  mcp/          MCP server for AI agents (stdio, admin API key)
  loadgen/      click generator for test campaigns
internal/
  ipindex/      CIDR index O(log n) + list manager with hot-reload
  config/       group/stream model + JSON loader (hot-reload)
  seplist/      separation lists in memory (hot-reload)
  geo/          MaxMind-format databases (City/Country + ASN), hot-reloaded
  cron/         the background jobs and their config
  atomicfile/   write-then-rename
  detect/       device + OS/browser/brand + bots, signatures
  router/       stream selection (predicates)
  render/       output macros + all redirect types
  fetch/        HTTP client with TTL cache ([REMOTE], CURL)
  store/        ClickHouse (logs/postbacks/stats) + Redis (counters/sessions)
  logbuf/       async event buffer → batch insert
  security/     argon2id, sessions, CSRF, constant-time
  server/       realip middleware (trusted proxies)
  admin/        HTTP handlers, file stores, embedded SPA (web/index.html)
  bridge/       Keitaro import (ktimport)
migrations/clickhouse/   schema (auto-applied by make infra-up)
deploy/                  docker-compose.yml (ClickHouse + Redis for local dev),
                         docker-compose.app.yml, install.sh, apply-edge.sh,
                         migrate-emtds.sh, watchdog.sh, nginx/
configs/                 example & test group configs
docs/                    USAGE / ARCHITECTURE / SECURITY / STATUS (+ *.ru.md),
                         wiki/ (Russian)
```

---

## Security

argon2id passwords · server-side sessions · CSRF on unsafe methods · login
rate-limit · trusted-proxy handling for XFF **and** `CF-IPCountry` ·
parameterized ClickHouse queries ·
strict `.dat` filename validation (no path traversal) · JSON-only input · secrets
via env only. Full model: [`docs/SECURITY.md`](docs/SECURITY.md).

---

## Roadmap

Not done yet: a second factor (TOTP) for the admin login, a settings page for
the api client, a fourth device category (Smart TV), version ranges in OS /
browser filters. See [`TODO.md`](TODO.md).

---

## License

[MIT](LICENSE).
