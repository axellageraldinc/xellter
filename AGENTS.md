# AGENTS.md — xellter

Server-wide rules: `~/AGENTS.md` (security first, scan before push, commit + push to `main`). This file only adds project specifics. `CLAUDE.md` just imports this file.

## What this is
xellter is a Go LLM gateway/router in front of kenari.id. A *client* (an app) owns one or more kenari keys; xellter generates the client's *presets* (`text`, `image`, `video`, `tts`, `stt`) from the models those keys can call, and resolves a preset to a real model per request with a ranking engine (live prices, promos, plans, free tiers, benchmark/JEV quality, reliability), failover, round-robin, RTK tool-output compression and an admin panel. Architecture: `docs/architecture.md`.

## Run / test
- Go lives at `~/.local/go-root/go/bin` (not on PATH by default). `scripts/ci.sh` sets it up.
- All checks: `./scripts/ci.sh` (gofmt, vet, tests, JS syntax). `make build` -> `bin/xellter`.
- **Tests must never call the network or paid APIs.** kenari is faked with `httptest` (`internal/app/app_test.go`). Keep it that way.
- Run locally: `./bin/xellter serve -config xellter.yaml` (copy `config.example.yaml`).

## Layout
```
cmd/xellter        CLI: serve, check, admin init, client add
internal/config    YAML schema, defaults, validation, history, secrets file
internal/catalog   kenari catalog/price/promo/plan sync -> immutable Snapshot
internal/store     SQLite (modernc, no CGO): request log, stats, sessions, JEV scores
internal/upstream  per-key limiter: rpm, free quota, plan ledger, cooldown, breaker, allow list
internal/rank      PURE ranking engine: gates -> components -> score -> main list
internal/route     attempts = model x key, round-robin, stickiness
internal/proxy     data plane: auth, body rewrite, streaming relay, usage accounting
internal/rtk       tool-output compression filters
internal/jev       System One quality judge (budget-capped)
internal/auth      admin login: scrypt + TOTP + recovery codes (aixell-compatible record)
internal/admin     /admin API + embedded UI: plain JS on vendored Tabler CSS, Inter font and a Tabler Icons subset (no build step, no CDN)
internal/app       wiring + hot reload (Apply) + background loops + preset generation (seedPresets)
```

## Deploy
User systemd unit `xellter.service` (`~/infra/systemd/user/`), listens on `127.0.0.1:8480`, exposed on the tailnet with `tailscale serve`. Runbook: `docs/operations.md`. Registry entry: `~/infra/REGISTRY.md`.

## Project rules
- **The panel shows only the knobs that matter** (ADR 0012). A kenari key has a name and nothing else; xellter detects the account, plan, tier, limits and the models a key may call. Presets have no model allow/deny list and no context-size gate. Client-level knobs (budget, RTK override, passthrough) keep their defaults. Anything else is changed in `xellter.yaml` (the Config tab edits the same file) — do not add a panel control for it.
- `skills/xellter/SKILL.md` documents the knobs that remain and how to change them. Read it before changing xellter's configuration.
- **Public repo (once opened): never commit** `xellter.yaml`, `data/`, `secrets.env`, API keys, client keys, auth records, request logs. They are git-ignored; keep it that way. Run the server-wide secret scan before every push.
- Hot path stays allocation-light and non-blocking: no per-request catalog fetch, no synchronous DB writes (logging is queued), streaming is copied through without buffering.
- `internal/rank` stays pure (no I/O). Every ranking decision must be explainable (`Candidate.Reason`, `Rejected`).
- Config is the single source of truth and is hot-reloaded through `app.Apply`; never mutate `*config.Config` in place after it has been published.
- **System calls use the account key.** Anything that needs an LLM but belongs to no client (JEV judging, future digests) goes through `Pool.SystemKey()` / `Key.ReserveSystem`, never through a client's key; log it as client `_system`.
- Never log prompts or responses. The request log stores metadata only.
- Decisions worth remembering go in `docs/adr/` (short, numbered). Phase status lives in `docs/plan.md`.
- Language: code, comments, docs, commits in English.
