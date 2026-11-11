# AGENTS.md

This file provides guidance to AI agents when working with code in this repository.

## Overview

gw2tools is a personal website for tracking things in Guild Wars 2. It is a single Go binary with a SQLite database, run locally during development and eventually in the homelab (`~/Projects/homelab`) as a container using the bjw-s `app-template` Helm chart. It sits on a private network behind the owner's GW2 API key and has no authentication.

### Features

- **Wardrobe** (`/wardrobe`): bought GWU unlocks waiting in the delivery box, then the cheapest missing Guaranteed Wardrobe Unlock (GWU) skins, finishers and minis on the trading post, mixed together as on immortius' analyser, whether there is a buy order on each, and whether that order is still the top bid. Also lists orders on missing skins outside the cheapest N (e.g. the price rose) and orders on skins that are already unlocked or bought.
- **Black Lion Sets** (`/black-lion`): every Black Lion weapon set, sorted by the cost to complete it (sets with skins not on the trading post come after, complete sets last). A ⚠ next to a total means some missing skins have no price yet, so the total is too low. Sets in the current Black Lion Chest get the Black Lion Chest icon next to their name. Each set expands (`<details>`) to its missing skins with the cheapest item, prices and order status, the same as the wardrobe page. A little JS keeps expanded sets open across the page's auto-refresh.

### Layout

- `main.go`: `serve` (default) and `seed` subcommands
- `internal/gw2`: GW2 API client
- `internal/store`: SQLite storage; schema in `migrations/`
- `internal/wardrobe`: orders and prices refresh (live API data), and view logic (ranking, order status, Black Lion sets)
- `internal/seed`: builds the static skin, finisher, mini, item and weapon set catalogue
- `internal/wiki`: reads the current Black Lion Chest sets from the GW2 wiki
- `internal/web`: HTTP handlers, `templates/`, and `static/` (embedded into the binary)

### Data

- **Catalogue (static)**: skins, finishers and minis, their GWU flag, and the tradeable items that unlock them. GWU membership comes from immortius' [Wardrobe Unlock Analyser](https://github.com/immortius/gw2-wardrobe-unlock) `content.json`, because the GW2 API does not expose it. Item-to-skin and item-to-mini mapping come from `/v2/items` (`default_skin`, `details.skins`, `details.minipet_id`); items don't name their finisher, so item-to-finisher comes from `/v2/finishers` (`unlock_items`). Loaded only by `gw2tools seed`.
- **Unlock kinds**: finishers and minis share the skin tables and code paths. The API's ids are only unique per kind (there is a skin 24 and a finisher 24), so every unlock is a `wardrobe.Key` of kind (`skin`, `finisher`, `mini`) and id, and the tables store a `kind` column next to each skin id. In the code, "skin" means any of the three. Only permanent finishers count as unlocked; ones with only charges left are still missing. Other GWU unlock types in `content.json` (dyes, outfits, gliders, mail carriers) are not tracked; adding one means a new kind, its `content.json` section in `seed.sections`, an item mapping, and an account endpoint in the orders refresh.
- **Black Lion weapon sets (static)**: the achievements in the "Black Lion Collections" category (`seed.BlackLionCategory`, 76) whose bits are all skins; the category also holds mini and toy collections. The " Weapon Collection" suffix is dropped from names. Set skins missing from `content.json` (a set newer than immortius' data) are fetched from `/v2/skins`.
- **New sets** release every 4–6 months, each with a new collection achievement. Re-running `gw2tools seed` picks them up with no code change (in the homelab: `kubectl -n default exec deploy/gw2tools -- /gw2tools seed`). Check the seed log's `sets` count went up. Skipped achievements (minis, toys, anything with a non-skin bit) are logged by name, so a new set with an unexpected shape shows up there. Sets are only ever added: a seed that finds fewer sets than are stored fails before the bulk item fetch and leaves the catalogue alone.
- **Account and market state (live)**, refreshed by the server:
  - **Orders refresh** (every minute at most): `/v2/account/skins`, `/v2/account/finishers`, `/v2/account/minis`, `/v2/commerce/transactions/current/buys`, `/v2/commerce/delivery`, and `/v2/commerce/listings` for catalogue items with an order (top bid, lowest sell, and tie depth). About 6–7 requests.
  - Skins with an item in the delivery box count as bought: they're listed in their own section, left out of the cheapest list, and not priced. Orders still open on them are listed with the redundant ones.
  - Inventory isn't tracked. The design assumes an item taken from the delivery box is unlocked within about a minute, before the next orders refresh. Until then its skin may briefly reappear as missing, or have no price until the next hourly pass; that's accepted.
  - **Prices refresh** (every hour at most): `/v2/commerce/prices` for items that unlock a missing GWU skin, finisher or mini, or Black Lion set skin, excluding items with orders. About 1,500 items at 200 per request, so about 8 requests at 200 ms spacing, shrinking as skins are unlocked. The start time of every run, including failed ones, is stored in `meta` (`prices_attempted_at`, the schedule clock), so restarts and failures don't trigger extra runs. Saving drops stored prices only for items no longer wanted; wanted items missing from a response keep their old price.
  - Unlocked skins accumulate in `account_skins` with `first_seen` and are never deleted. Items for unlocked skins are never priced.
  - **Black Lion Chest check** (once a day, on the first tick after midnight UTC, which gives the wiki time to catch up with a game update; a server that was down at midnight checks on start): one MediaWiki API request for the wiki's "Black Lion Chest" page source. The sets are the `* [[<Name> weapon skins]]` entries in its Contents section, matched to set names ignoring case and apostrophe style. Names are stored in `meta` (`chest_sets`), with the attempt time as the schedule clock like prices. A page that lists no sets is an error and keeps the old list. If the wiki changes that page's layout, `wiki.ParseChestSets` is the one place to update.
  - Minimum intervals are enforced in code (`web.MinOrdersInterval`, `web.MinPricesInterval`, and the daily chest check). Don't add on-demand refresh that bypasses them.
- Only skins with a tradeable item count toward the page's GWU totals. immortius' main count includes skins that can't be bought on the trading post.
- `seed` downloads `content.json` (about 7 MB), reads the Black Lion sets (2 requests, plus `/v2/skins` for any set skins `content.json` lacks) and the finishers (1 request), and fetches details for every tradeable item (about 135 requests). It replaces the catalogue and leaves account state alone. `--content` also accepts a local file.
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
| `GW2TOOLS_WIKI_URL` | `https://wiki.guildwars2.com` | Wiki base URL for the daily Black Lion Chest check |

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
- Game assets: `static/black-lion-chest.png` is the in-game Black Lion Chest icon (item 20316), downloaded once from the render service with the owner's approval and used under ArenaNet's content terms for non-commercial fan sites. Add other game assets the same way: downloaded by hand, committed, and noted here.
- Keep the frontend simple: server-rendered `html/template` and plain CSS. JavaScript is optional enhancement only; pages must work without it.
- Ask before accessing wiki.guildwars2.com. The one approved use is the server's Black Lion Chest check: one page request a day.
- Never put the API key in code, config, or logs. It comes from the `GW2_API_KEY` environment variable.
