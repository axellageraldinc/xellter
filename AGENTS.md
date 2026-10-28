# AGENTS.md — xellter

Server-wide rules: `~/AGENTS.md` (security first, scan before push, commit + push to `main`). This file only adds project specifics. `CLAUDE.md` just imports this file.

## What this is
xellter is a Go LLM gateway in front of kenari.id. A *client* (an app) owns one or more kenari keys, one purpose and at most one preset per category (several for chat); a *preset* is an explicit pool of catalog models plus one optimization (Cheapest, Balanced, Best Quality, Fastest). xellter orders that pool deterministically, routes round-robin over the top ten with a session pin, retries a retriable failure once, evicts a model from one preset after three errors, compresses tool output with RTK, and serves an admin panel. Architecture: `docs/architecture.md`. The accepted design is `docs/rewrite-plan.md`.

## Run / test
- Go lives at `~/.local/go-root/go/bin` (not on PATH by default). `scripts/ci.sh` sets it up.
- The panel is React in `web/`: `cd web && npm install && npm run build` writes `internal/admin/ui/dist`, which the Go binary embeds. `scripts/ci.sh` builds it first, so a panel that does not compile fails the build.
- All checks: `./scripts/ci.sh` (panel build, gofmt, vet, tests). `make build` -> `bin/xellter`.
- **Tests must never call the network or paid APIs.** kenari and every leaderboard source are faked with `httptest`. Keep it that way.
- Run locally: `./bin/xellter serve -config xellter.yaml` (copy `config.example.yaml`). The panel is at the root, its API at `/api`. `./bin/xellter explain` prints the current ranking with reasons.

## Layout
```
cmd/xellter        CLI: serve, check, migrate, explain, admin init, client add, key add
internal/config    YAML schema, defaults, validation, retired-key refusal, history, secrets, migration
internal/catalog   kenari catalog/price/promo/plan sync -> immutable Snapshot (System One on its own endpoint)
internal/store     SQLite (modernc, no CGO): request log, usage aggregates, leaderboard rows, admin sessions
internal/upstream  per-key and per-account limits: rpm, free quota, plan ledger, cooldown, breaker, allow list
internal/rank      PURE ranking of one preset: pool -> rejections -> cost/quality -> top list + reasons
internal/route     rankings cache, round-robin ring, session pin, per-preset eviction
internal/effort    what each model is known to do with a reasoning effort, measured from traffic (ADR 0026) and saved across restarts (ADR 0027)
internal/proxy     data plane: auth, body rewrite, streaming relay, usage accounting, read-only endpoints
internal/rtk       tool-output compression, always on, no configuration
internal/bench     public leaderboards -> 0..1 per model and arena
internal/auth      admin login: scrypt + TOTP + recovery codes (an existing record can be reused)
internal/admin     the panel: /api JSON API + the embedded React app (built by Vite, no CDN)
internal/app       wiring + hot reload (Apply) + background loops
web                the panel's source: React, Vite, Tailwind, shadcn/ui; builds into internal/admin/ui/dist
```

## Deploy
User systemd unit `xellter.service`, listens on `127.0.0.1:8480`, exposed on the tailnet with `tailscale serve`. Runbook: `docs/operations.md`.

## Project rules
- **The panel shows only what matters** (ADR 0012). A kenari key has a name and nothing else; xellter detects the account, plan, tier, limits and the models a key may call. The panel offers purpose, keys, and per preset the optimization, the sticky-session toggle and the model picker. Anything else is edited in `xellter.yaml` (the Config tab edits the same file) — do not add a panel control for it. There is no Settings page and no "Advanced" block (ADR 0014).
- **A preset is named by the owner, one per category, except chat** (ADR 0021, 0038). The name is the model the app asks for; the `category` says what it serves, and validation refuses a second preset of a kind, except for chat, where a client may hold several. Never generate presets, never seed a pool.
- **Pools are explicit, except for free and discounted models** (ADR 0019, 0022). A paid model is routed only because it is in a preset's `models`. Every free model and every model whose promo is running is added to the pool of every preset of that category automatically, and leaves it again when the promo ends or kenari drops the model. The owner can untick one per preset; it goes into the preset's `excluded` list and is never added again (ADR 0035). The old capability gates (`require`), quality floor (`min_quality`), weights, strategies and `cost_profile` are gone, and their config keys are refused on load (ADR 0017). `passthrough` globs are gone too; being in a pool is the allow list, and the old key is silently ignored.
- **The panel is the site** (ADR 0023). It is served at the root of its host, its API at `/api`, the proxy at `/v1`. The panel's source is `web/` (React, Vite, Tailwind, shadcn/ui); `npm run build` writes `internal/admin/ui/dist`, which the Go binary embeds. No CDN, ever: the browser loads nothing from a third party. The design tokens are the three layers of the ui-ux-pro-max reference (primitive, semantic, component) in `web/src/styles/tokens.css`.
- **RTK has no switch.** It is always on, all filters, fixed thresholds (`internal/rtk` constants). Never add a config key or a request header for it.
- **Scoring is deterministic and fetched.** `internal/rank` stays pure: no I/O, no model calls, no randomness. Quality comes from public leaderboards downloaded by `internal/bench` — never typed by the owner, never produced by asking a model to grade another (ADR 0016, 0018). Every candidate and every rejection carries a reason string.
- **A purpose reorders, it never filters.** A model with no purpose-specific score falls back to the general arena and is flagged `general score`; a model with no evidence anywhere is `unranked`, which only excludes it from Best Quality (ADR 0018).
- **Eviction is per preset.** Three failures inside `routing.evict_window_minutes` take a model out of that client's preset only, for `routing.evict_cooldown_minutes`, and a session pinned to it moves. Never make eviction global, never persist it, never let a 5xx trip a key's breaker (ADR 0020).
- **Stickiness is a per-preset toggle** (ADR 0025). A preset carries `sticky`; absent means sticky, `sticky: false` routes every turn again. The global `routing.sticky_session` is gone and is refused on load. Never add a global sticky knob back.
- **Never log prompts or responses.** The request log stores metadata only: timestamps, model, key name, tokens, cost, status, latency, session id.
- **`data/` is live state, never a scratch directory.** It holds the request log, the leaderboard scores and the admin sessions. Never `rm`, `mv` or `rm -rf` anything under it, and never run a cleanup command whose path could resolve to it: use an absolute path, and use a throwaway directory outside the repo for experiments. A deleted database comes back empty and the usage history is gone. `data/xellter.db.marker` makes that loss visible in the log, and `xellter-backup.timer` snapshots the database, the secrets and the config to `xellter-backups/` every day — outside `data/`, so the backup survives the loss it exists for.
- **Public repo: never commit** `xellter.yaml`, `data/`, `secrets.env`, API keys, client keys, auth records, request logs. They are git-ignored; keep it that way. Run the server-wide secret scan before every push.
- Hot path stays allocation-light and non-blocking: no per-request catalog fetch, no synchronous DB writes (logging is queued), streaming is copied through without buffering.
- Config is the single source of truth and is hot-reloaded through `app.Apply`; never mutate `*config.Config` in place after it has been published.
- Decisions worth remembering go in `docs/adr/` (short, numbered). Phase status lives in `docs/plan.md`.
- Language: code, comments, docs, commits in English.
