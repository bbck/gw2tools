# AGENTS.md

This file provides guidance to AI agents when working with code in this repository.

## Overview

gw2tools is a personal website for tracking things in Guild Wars 2. It is a single Go binary with a SQLite database, run locally during development and eventually in the homelab (`~/Projects/homelab`) as a container using the bjw-s `app-template` Helm chart. It sits on a private network behind the owner's GW2 API key and has no authentication.

### Features

- **Wardrobe** (`/wardrobe`): bought GWU skins waiting in the delivery box, then the cheapest missing Guaranteed Wardrobe Unlock (GWU) skins on the trading post, whether there is a buy order on each, and whether that order is still the top bid. Also lists orders on missing skins outside the cheapest N (e.g. the price rose) and orders on skins that are already unlocked or bought.

### Layout

- `main.go`: `serve` (default) and `seed` subcommands
- `internal/gw2`: GW2 API client
- `internal/store`: SQLite storage; schema in `migrations/`
- `internal/wardrobe`: orders and prices refresh (live API data), and view logic (ranking, order status)
- `internal/seed`: builds the static skin and item catalogue
- `internal/web`: HTTP handlers, `templates/`, and `static/` (embedded into the binary)

### Data

- **Catalogue (static)**: skins, their GWU flag, and the tradeable items that unlock them. GWU membership comes from immortius' [Wardrobe Unlock Analyser](https://github.com/immortius/gw2-wardrobe-unlock) `content.json`, because the GW2 API does not expose it. Item-to-skin mapping comes from `/v2/items`. Loaded only by `gw2tools seed`.
- **Account and market state (live)**, refreshed by the server:
  - **Orders refresh** (every minute at most): `/v2/account/skins`, `/v2/commerce/transactions/current/buys`, `/v2/commerce/delivery`, and `/v2/commerce/listings` for skin items with an order (top bid, lowest sell, and tie depth). About 4–5 requests.
  - Skins with an item in the delivery box count as bought: they're listed in their own section, left out of the cheapest list, and not priced. Orders still open on them are listed with the redundant ones.
  - Inventory isn't tracked. The design assumes an item taken from the delivery box is unlocked within about a minute, before the next orders refresh. Until then its skin may briefly reappear as missing, or have no price until the next hourly pass; that's accepted.
  - **Prices refresh** (every hour at most): `/v2/commerce/prices` for items that unlock a missing GWU skin, excluding items with orders. About 90 requests at 200 ms spacing, shrinking as skins are unlocked. The start time of every run, including failed ones, is stored in `meta` (`prices_attempted_at`, the schedule clock), so restarts and failures don't trigger extra runs. Saving drops stored prices only for items no longer wanted; wanted items missing from a response keep their old price.
  - Unlocked skins accumulate in `account_skins` with `first_seen` and are never deleted. Items for unlocked skins are never priced.
  - Minimum intervals are enforced in code (`web.MinOrdersInterval`, `web.MinPricesInterval`). Don't add on-demand refresh that bypasses them.
- Only skins with a tradeable item count toward the page's GWU totals. immortius' main count includes skins that can't be bought on the trading post.
- `seed` downloads `content.json` (about 7 MB) and fetches details for every tradeable item (about 135 requests). It replaces the catalogue and leaves account state alone. `--content` also accepts a local file.
- Schema changes go in a new `internal/store/migrations/NNN_*.sql` file (applied in order, tracked by `PRAGMA user_version`). Never edit an applied migration.
- The GW2 API rate limit is believed to be a token bucket (about 300 burst, refilling at 5 per second; not verified). The client spaces requests by 200 ms and retries HTTP 429 with backoff.

### Configuration

| Variable | Default | |
| --- | --- | --- |
| `GW2_API_KEY` | | API key with `account`, `unlocks`, `tradingpost` |
| `GW2TOOLS_DB` | `gw2tools.db` (`/data/gw2tools.db` in the image) | SQLite path |
| `GW2TOOLS_LISTEN` | `:8080` | Listen address |
| `GW2TOOLS_ORDERS_INTERVAL` | `1m` (minimum `1m`) | Unlocks and buy orders refresh |
| `GW2TOOLS_PRICES_INTERVAL` | `1h` (minimum `1h`) | Candidate item prices refresh |

## Commands

All tooling is pinned in `mise.toml`; use the tasks rather than calling tools directly.

- `mise run test`: run all tests
- `mise run lint`: gofmt check and `go vet`
- `mise run dev`: run the server locally at http://127.0.0.1:8080, with `GW2_API_KEY` taken from 1Password (`op://Homelab/gw2tools/GW2_API_KEY`) via `op run`
- `mise run seed`: load the catalogue into the local database
- `mise run build` / `mise run docker-build`: build the binary or the image

## Release

- `.github/workflows/ci.yaml` lints and tests every PR and push. It builds the image for `linux/amd64` and `linux/arm64`.
- Pushes to `master` publish `ghcr.io/bbck/gw2tools:rolling` and `:sha-<commit>`, with a build provenance attestation.
- The homelab pins `rolling@sha256:...`; Renovate opens a digest-update PR there when `rolling` moves.
- Renovate (`.renovaterc.json5`) updates Go modules, the Go toolchain (grouped across `mise.toml`, `go.mod` and the Dockerfile), Dockerfile base images and Action pins. Non-major updates to 1.0+ dependencies automerge once CI passes.
- The runtime image is `scratch`: the static binary, CA certificates, and an empty `/data` owned by 65534. Anything else the binary needs at runtime must be embedded or copied in explicitly.

## Rules

- NEVER dynamically scrape bulk data. The server only makes targeted requests (account data, and prices for items relevant to the account). If seeding data is needed, write a script or subcommand that the owner runs by hand, like `gw2tools seed`.
- Always write tests for features, and verify `mise run test` passes after making changes.
- The frontend only needs to support dark mode.
- No external asset URLs. All CSS, JS, fonts, and any frameworks must be embedded in and served by the application. This includes GW2 render-service icons.
- Keep the frontend simple: server-rendered `html/template` and plain CSS. JavaScript is optional enhancement only; pages must work without it.
- Ask before accessing wiki.guildwars2.com.
- Never put the API key in code, config, or logs. It comes from the `GW2_API_KEY` environment variable.
