# AGENTS.md — xellter

Server-wide rules: `~/AGENTS.md` (security first, scan before push, commit + push to `main`). This file only adds project specifics. `CLAUDE.md` just imports this file.

## What this is
xellter is a Go LLM gateway/router in front of kenari.id: preset models (`balanced`, ...) are resolved to real models by a ranking engine (live prices, promos, plans, free tiers, benchmark/JEV quality, reliability), with failover, round-robin, per-client keys, RTK tool-output compression and an admin panel. Architecture: `docs/architecture.md`.

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
internal/rank      PURE ranking engine: gates -> components -> score -> trial/main lists
internal/route     attempts = model x key, round-robin, stickiness, canary
internal/proxy     data plane: auth, body rewrite, streaming relay, usage accounting
internal/rtk       tool-output compression filters
internal/jev       System One quality judge (budget-capped)
internal/auth      admin login: scrypt + TOTP + recovery codes (aixell-compatible record)
internal/admin     /admin API + embedded UI (vanilla JS, no build step)
internal/app       wiring + hot reload (Apply) + background loops
```

## Deploy
User systemd unit `xellter.service` (`~/infra/systemd/user/`), listens on `127.0.0.1:8480`, exposed on the tailnet with `tailscale serve`. Runbook: `docs/operations.md`. Registry entry: `~/infra/REGISTRY.md`.

## Project rules
- **Public repo (once opened): never commit** `xellter.yaml`, `data/`, `secrets.env`, API keys, client keys, auth records, request logs. They are git-ignored; keep it that way. Run the server-wide secret scan before every push.
- Hot path stays allocation-light and non-blocking: no per-request catalog fetch, no synchronous DB writes (logging is queued), streaming is copied through without buffering.
- `internal/rank` stays pure (no I/O). Every ranking decision must be explainable (`Candidate.Reason`, `Rejected`).
- Config is the single source of truth and is hot-reloaded through `app.Apply`; never mutate `*config.Config` in place after it has been published.
- Never log prompts or responses. The request log stores metadata only.
- Decisions worth remembering go in `docs/adr/` (short, numbered). Phase status lives in `docs/plan.md`.
- Language: code, comments, docs, commits in English.
