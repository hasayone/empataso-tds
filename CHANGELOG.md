# Changelog

All notable changes to EmTDS are recorded in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html). Every noticeable change goes into `## [Unreleased]` first; the update agent shows the added lines of this file as "What's new" before an update is applied.

## [Unreleased]

### Added

- Assistant budget in US dollars. On the «Usage» tab of the assistant settings every user has a «Budget, USD» field right of the amount spent, and a block of groups (roles) appears below the users when the system has roles. An empty field means no limit and a negative value is refused. The budget counts only money of the UTC day (the price of the tokens), not chat length or tokens, and is saved with the other assistant settings (`user_budget_usd`, `group_budget_usd` in `PUT /api/ai/settings`). A turn that would pass the budget of the user or of their group, the stricter one wins, is refused with `ai_quota` (`params.scope`: `user_budget` or `group_budget`) and the chat tells which. Save stays visible on the «Usage» tab.
- Chips of the allowed models carry actions. On hover or keyboard focus a chip shows a cross that takes the model off the selection (its prices go too) and, for a model that is not the default, a star that makes it the default. Taking off the default hands it to the next selected model by name (case ignored), or to the first by name when it was the last. Nothing is saved until "Save".
- Assistant replies link to records. The assistant writes a campaign, offer, landing, affiliate network or traffic source as `[[type:id|Name]]`, and the panel shows it as a link to the page of that record (`#/flows/{id}`, `#/offers/{id}`, `#/landings/{id}`, `#/networks/{id}`, `#/sources/{id}`). Any other text of a reply stays plain, a broken token stays text, and older replies show as before.
- Keitaro check shows the tracker version. The API does not report it, so "Check connection" reads a few fresh click and conversion log records (read-only) and votes: SubID format (dotted is Keitaro 11), id columns (`click_id`/`conversion_id` vs `event_id`/`event_type`), Rebill (10) and custom conversion types (11). The card says "Keitaro 10" or "Keitaro 11" with the signs, or that the version is not determined when the signs are missing or disagree. Log import then asks the id columns of the found version, reads them into the click and conversion rows, and takes the conversion type from `event_type` when `status` is empty.
- A clear button in the search fields of campaigns, offers, landings, networks, sources and domains. It shows only while the field has text and clears the field in one click.
- Voice input in the assistant message field. A microphone button beside "Send" starts dictation in the language of the interface (ru-RU, en-US, uk-UA) and the same button stops it. Each confirmed phrase is appended to the text after a space for editing; the draft shows only beside the button, and nothing is sent by itself. One click starts a recording that lasts until you press stop: the microphone button turns into the stop button, the recogniser restarts by itself when the browser closes its session, and animated voice waves follow the loudness of the microphone (still bars with reduced motion). A failure (no microphone access, speech not recognised) is shown in full and only when the whole session brought no phrase. Chrome and Edge use the browser Web Speech API.
- Dictation in a browser without its own speech service. When the browser answers `network` or `service-not-allowed` (the embedded Electron browser) or has no Web Speech API (Firefox), dictation goes on inside the page: the recording is turned into text by Whisper tiny through transformers.js. The audio does not leave the browser, there is no paid API and no server of ours. The model (about 40 MB) downloads once into the browser cache with a progress line and is not in the repository. The browser is remembered and the next dictation goes straight to this mode. No message about the internet is shown.
- Header marks for the administrator from one answer, `GET /api/system/signals` (`update`, `down`). A newer commit for the running build puts a yellow dot on the avatar and the download icon on the «About» entry of the profile menu. An engine, ClickHouse or Redis that does not answer puts a red dot and a red triangle there, and opens a window once per set of failed services: which ones, a request to restart the server or the service and a link to «Settings → About». The marks are read once a minute.
- «Settings → About» marks each build of the admin, the engine and cron as Production or Development. Production comes only from `EMTDS_ENV=production`, which `install.sh` writes to `/etc/emtds/server.env`. A local stack is Development. The field is `env` in `GET /api/version` and in `/internal/version` of the engine.
- `GUID_INSTALL_UBUNTU.MD`: installing on a clean Ubuntu step by step (Docker, git, nginx, the clone in `/opt/emtds`, `deploy/install.sh --repo … --channel main`, the checks and the first login).
- Daily ceilings of the assistant on the «Usage» tab under the cards: tokens a day for everyone and for one user, US dollars a day for everyone and for one user. An empty field or «No limit» removes a ceiling; tokens have no ceiling by default. In `PUT /api/ai/settings` `null` means no limit and `0` is a real ceiling that refuses every turn. A card without a ceiling does not show «of».
- Bot signature database. The `ch_ua` check (on in the default profile, so every stream gets it without a switch) also matches the User-Agent against open bot lists downloaded by the new cron job `bot_signatures`: Matomo DeviceDetector `bots.yml`, Crawler User Agents and isbot. A match gives the label `sign_db` and the header `X-Emtds-Bot-Reason` with the source and the rule. Built-in signatures and search engines are checked first, so a search engine User-Agent is still confirmed by its IP list or DNS as before; weak generic words never make a bot, and a VPN, a proxy, an ASN or an empty referer are not part of this check. «Automation → Bot signatures» (on, every 24 hours) has «Run now», the status, the last success, the number of signatures and the sources to add, edit, remove, switch on and off with their errors. Rules are validated and deduplicated before they are written to `bot_signatures.json` in the data directory; a failing source keeps its previous rules and a broken download never replaces the working database. The engine swaps the database in memory without stopping clicks and does not touch the network on a click. FingerprintJS BotD is not used: it needs JavaScript on every visitor's page.

### Changed

- The «Update» button of «Settings → About» is always drawn, highlighted and above the report of the last run. When it cannot be used the reason is written under it, for example that a Keitaro import is running: the update waits for it and the import is not interrupted; the page rechecks every 15 seconds and the button turns on when the import ends. The «New version available» banner has the same «Update» action (the same request into the inbox, the updater on the host pulls git) and can be hidden in this browser until the next version or for 24 hours; the avatar dot stays. When the admin does not confirm idle within 10 minutes the update run now says that an import or a catch-up of Keitaro is running and nothing was applied, instead of suggesting `--keitaro-idle`.
- Assistant settings, allowed models: the ticked models and the default one show as chips above the model list and update at once, before Save. The prices of the ticked models are a form block per model with labelled «Input» and «Output» fields instead of a table, with «Input» and «Output» side by side. The limits moved from their own tab to a folded «Limits» block under the prices.
- The list of fields (hidden metrics in the access editor, filters, columns) follows one order by group: campaign, time, result, money, geo, device, traffic source, identifiers, network, origin. The «Hidden metrics» heading in the access editor has a top margin like the other headings.
- The period button of the header shows the chosen period next to the calendar icon. When the header does not fit, the nav labels hide first, then the period label, and only then the nav folds into the menu button. The period stays in the title and the accessible name of the button.
- Assistant limits: the per-turn limits stay in the folded «Limits» block under the prices, and the daily ceilings moved to the «Usage» tab.
- On a narrow screen (up to 720px) the «Create» button in the lists of campaigns, offers, landings, networks, sources and domains shows only «+»; the name stays for screen readers.

### Fixed

- A long Keitaro click or conversion log import no longer fails on one broken page. A page whose answer breaks off mid-body, does not connect, or comes back 429, 502, 503, 504 or Cloudflare 520–524 is asked again after 2, 10 and 30 seconds; only then does the import stop. A refused column list (400) still falls back at once. The error now names the cause, for example `keitaro: чтение /admin_api/v1/clicks/log: unexpected EOF`.
- AI settings, "Connection": the note about the key storage spans the full width of the field grid under the fields instead of half of it.
- Keitaro transfer result: the "finished" note no longer touches the counters above it, the warning table wraps its columns inside the panel at about 580 px without side scroll, and the counters wrap too. An offer country `Unknown` (or an empty code) is kept as the country "Unknown" and is no longer reported as an approximation; real garbage still is.
- The campaign filter bar: the group filter has the same width as the state filter, the search field takes the rest of the row, and the bar stays in one row at about 740 px.
- The Markdown button of the API reference opens a formatted page inside the admin (contents, highlighted samples, downloads of the `.md`, OpenAPI and Postman files) instead of the raw text in a new tab.
- The dialog that shows a new API key once: clicking the key (or Enter/Space on it) copies it the same way as the «Copy» button, the field spans the dialog width and wraps long keys, and after copying the button reads «Copied» for two seconds.

## [v0.1.0] - 2026-11-17

First tagged version: the current state of the product.

### Added

- **Engine:**
  - Campaigns and flows with streams, conditions and 18 output types.
  - Click handling with uniqueness by IP or by cookie, anti-flood and per-stream impression limits.
  - Postbacks and conversions.
  - Bot protection: user-agent and referer signatures, search-engine IP lists, reverse DNS, separate bot output.
  - Landings and offers.
- **Admin panel (embedded in the binary):**
  - Reports and dashboard with filters, sorting and saved table settings.
  - Users, roles and per-section permissions.
  - API keys.
  - Domains parked as a list, with the Cloudflare integration (zones, DNS records, `ssl` and `always_use_https` settings).
  - Transfer from Keitaro.
  - Integrations.
  - Telegram notifications sent by the background cron service.
  - OpenAPI, Postman collection and `/llms.txt`, generated from one route catalog.
- **Update agent** (`deploy/update.sh`):
  - Applies only fast-forward updates and fetches only from the pinned `origin`.
  - Checks for updates every hour.
  - Shows "What's new" taken from this file.
  - Optional commit and tag signature check against `/etc/emtds/allowed_signers`; off by default.
- **One-command installer** from git: `deploy/install.sh` clones the repository, builds the images with the commit baked in and starts the stack.
- **First-login token:** the installer prints a one-time token that is required to create the first administrator.
- **Settings → About:** version, build commit, update state and server state.
- **Theme:** light and dark themes plus the brand colour scheme of the admin panel.

### Changed

- The product is branded as EmTDS: directories `/opt/emtds` and `/etc/emtds`, Docker Compose projects `emtds-infra` and `emtds-app`, environment variables `EMTDS_*`.
- The "Profile" menu item is renamed to "Settings".
- The menu and the Settings tabs are built from one list, so they always match.

### Security

- Passwords are hashed with argon2id; sessions are stored on the server; CSRF protection and parameterized queries are used throughout.
- Access to the admin panel and to API keys is limited by roles and sections.
- The admin container has no `docker.sock` and no root access to the host; updates run only through the host agent.
- A secrets scan (gitleaks, pinned version) runs in CI on new commits.

[Unreleased]: https://github.com/hasayone/empataso-tds/compare/v0.1.0...HEAD
[v0.1.0]: https://github.com/hasayone/empataso-tds/releases/tag/v0.1.0