# Architecture

```
client ──(xl-… key)──▶ proxy ──▶ router ──▶ upstream key pool ──▶ kenari.id
                         │          │              ▲
                         │          └─ rankings ◀──┴── catalog (prices, promos, plans) + store (health, JEV)
                         └─ RTK, usage accounting, request log (SQLite, async)
admin panel ──▶ app.Apply(config) ──▶ hot reload of pool, router, proxy
```

## Request flow (`internal/proxy`)
1. Authenticate: `Authorization: Bearer` or `x-api-key` -> sha256 -> client. Unknown 401, disabled 403.
2. Read the body (bounded), find `model` (JSON or multipart). Check the client's daily budget.
3. `route.Plan` turns the requested name into at most `max_attempts` attempts (model × key):
   - a preset model -> its ranking (see below); a real model name is only allowed through the client's `passthrough` globs;
   - order: sticky (same session -> same model/key), canary (new model, `canary_percent`), the round-robin front group rotated, the rest by score;
   - for each model, usable keys are ordered: sticky key, then plan-covered/free, then paid; equal keys rotate.
4. RTK compresses tool outputs (chat only). For streamed chat completions `stream_options.include_usage` is injected.
5. For each attempt: `Key.Reserve` (rpm, free quota) -> rewrite `model` -> send. First-byte timeout is enforced per request.
6. Success: stream the body to the client in 16 KB chunks with a flush each (no buffering), keeping only the head and tail to read the `usage` object. Failure before the first byte: `Key.Failure` classifies the status (429/402/401/403/400/5xx/network), applies cooldown/block/breaker/allow-list learning and moves on; errors that are the client's fault (other 400s) are returned as is.
7. Log metadata (async), update key ledgers, remember the session's model, add to the client's spend.

## Ranking (`internal/rank`)
Per auto preset, for every catalog model of the preset's modality:

1. **Gates** (pass/fail): allow/deny globs, tool calling, vision, reasoning, min context, not sunsetting within `avoid_sunset_days`, subscriber-only needs a plan key, at least one enabled key may use it.
2. **Components** (each 0..1):
   - `cost`: price of a reference request (`cost_profile`) from the live catalog, including a running promo, a scheduled price change inside the lookahead, cache discounts, zero if the model is free, and `plan_cost_factor` × list price if a plan key that still has budget covers it. Log-scaled across candidates; free = 1.
   - `benchmark`: configured score/100, else a price-percentile proxy computed from *undiscounted* prices (a promo never makes a model look weaker; a `:free` twin uses its paid sibling), else 0.5.
   - `jev`: stored System One score; unscored models get 0.4 while JEV is enabled.
   - `reliability`: success rate and latency vs the median over `stats_window_hours` (0.6 until 5 samples).
   - `context`: log scale between `min_context` and 16× that.
3. **Score** = weighted average of the components with weight > 0 (per-preset `weights`), plus bonuses: free, plan-covered, promo (scaled by percent off, only when ≥ `promo_min_remaining_hours` remain).
4. **Quality gate**: weighted average of benchmark, jev and context must reach `min_quality`; a JEV score below `jev.pass_threshold` rejects the model.
5. **Trial**: a model first seen within `new_model_days` that has not yet proven itself (`canary_min_requests` at `canary_min_success`) is excluded from the main list and only receives canary traffic; it may not cost more than 3× the leader's list price (floor Rp0.7).
6. The top `max_candidates` form the main list; those within `rr_band` of the leader are the round-robin front group (`strategy: round_robin`).

Rankings are recomputed every 30 s, after each catalog sync and after each config change, and cached; requests only read them. Every candidate carries its components, bonuses, flags and a reason string, and rejected models carry the reason (shown in the panel).

## Accounts and keys (`internal/upstream`)
kenari applies rate limits, free quotas and plan budgets **per account**, not per key (https://kenari.id/docs/rate-limits). xellter therefore groups keys by `account` and keeps this state on the account: sliding-window rpm, free-model quota counted since 00:00 UTC (tier defaults from kenari: free 5 rpm / 50 per day, topup 10 / 1000, plan 25 / unlimited), plan budget, balance flag. Error codes map to account effects: `free_quota_daily` pauses free models until 00:00 UTC, `free_quota_rpm` for `Retry-After`, `plan_limit_reached` turns plan coverage off, `rate_limit_exceeded` cools the whole account, `402` pauses paid models. Per key: cooldown when the key is rejected (401/403), block of a (key, model) pair after upstream errors with exponential backoff, circuit breaker after consecutive failures, allow list.

**Plan numbers without hardcoding.** `GET /v1/account/quota` and `/v1/account/balance` work only with an *unrestricted* key (restricted keys get 403 `restricted_key_not_allowed`). xellter tries them with every key at start-up and every 5 minutes. When one key of an account is unrestricted, the plan name, the 5 h / weekly / monthly budgets with used and remaining Rupiah and their reset times, and the balance come straight from kenari (our own spend since the last read is added on top); the configured `plan` is only a fallback. Without an unrestricted key the plan budget is estimated from the request log against the catalog plan prices.

kenari rejects unknown or disallowed models with `400 … Allowed models: …`, which is free. xellter sends one such probe per key at startup, after config changes and every 6 h, so restricted keys are only used for the models they may call.

## State
| What | Where |
|---|---|
| config (clients, presets, rules, benchmarks) | `xellter.yaml` (0600), history in `data/config-history/` |
| upstream key values | `data/secrets.env` (0600) |
| request log, JEV scores, first-seen dates, sessions, video job -> key | `data/xellter.db` (SQLite, WAL) |
| last good catalog | `data/catalog.json` (used when kenari is unreachable) |
| admin login | `data/auth-record.json` (0600), `login-state.json`, `recovery-used.json` |

Counters (rpm, free quota, plan ledger, client spend) are rebuilt from the request log at start-up.

## Failure behaviour
- kenari catalog unreachable: keep serving with the last snapshot (flagged stale in the panel).
- No candidate right now: `503 no_candidates`/`no_capacity` with `Retry-After`; client budget used: `429 budget_exceeded`.
- Streaming failure after the first byte cannot be retried; it is logged as an error.
- Panics in RTK are caught and the original text is sent.
