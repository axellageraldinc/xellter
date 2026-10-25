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
   - a name in the client's own `presets` -> that client's ranking of that preset (see below); any other real model name is only allowed through the client's `passthrough` globs;
   - order: sticky (same session -> same model/key), the round-robin front group rotated, the rest by score;
   - for each model, the client's usable keys are ordered: sticky key, then plan-covered/free, then paid; equal keys rotate. A client never uses another client's keys.
4. RTK compresses tool outputs (chat only). For streamed chat completions `stream_options.include_usage` is injected.
5. For each attempt: `Key.Reserve` (rpm, free quota) -> rewrite `model` -> send. First-byte timeout is enforced per request.
6. Success: stream the body to the client in 16 KB chunks with a flush each (no buffering), keeping only the head and tail to read the `usage` object. Failure before the first byte: `Key.Failure` classifies the status (429/402/401/403/400/5xx/network), applies cooldown/block/breaker/allow-list learning and moves on; errors that are the client's fault (other 400s) are returned as is.
7. Log metadata (async), update key ledgers, remember the session's model, add to the client's spend.

## Ranking (`internal/rank`)
Per client and auto preset, for every catalog model of the preset's modality that at least one of the client's keys may call (duplicates merged, a model reachable by several keys is one candidate):

1. **Gates** (pass/fail): tool calling, vision, reasoning, not sunsetting within `avoid_sunset_days`, subscriber-only needs a plan, at least one of the client's keys may use it.
2. **Components** (each 0..1):
   - `cost`: price of a reference request (`cost_profile`; for text 4000 input tokens, 70% of them cache hits, 600 output tokens) from the live catalog: running promo and scheduled price changes included, cache discount applied, zero if the model is free, `plan_cost_factor` × list price if a plan that still has budget covers it. Score: free = 1, otherwise `1 / (1 + ln(cost / cheapest))` where the reference is the cheapest paid candidate (equal price 1.0, 13% dearer 0.89, twice 0.59, ten times 0.30).
   - `benchmark`: your configured score, else the Arena leaderboard score (a win probability against the arena's median model, taken from the lower end of the 95% interval so thinly measured models must prove themselves; see `internal/bench`), else a price-percentile estimate, else 0.5. Previously: configured score/100, else a price-percentile proxy computed from *undiscounted* prices (a promo never makes a model look weaker; a `:free` twin uses its paid sibling), else 0.5.
   - `jev`: stored System One score; unscored models get 0.4 while JEV is enabled.
   - `reliability`: success rate and latency vs the median over `stats_window_hours` (0.6 until 5 samples).
   - `context`: log scale between a fixed baseline of 8000 tokens and 16× that (a soft preference; there is no context gate).
3. **Score** = weighted average of the components with weight > 0 (per-preset `weights`), plus bonuses: free, plan-covered, promo (scaled by percent off, only when ≥ `promo_min_remaining_hours` remain). The bonuses are multiplied by how much the preset cares about cost (its cost weight share ÷ 0.30, between 0.25 and 2): Cheapest doubles them, Balanced keeps them, Best quality cuts them to a third, so "free" cannot outvote quality when quality is the goal.
4. **Quality gate**: weighted average of benchmark, jev and context must reach `min_quality`; a JEV score below `jev.pass_threshold` rejects the model.
5. **Unverified models are flagged, not held back.** A text model with no evidence of quality (no configured benchmark, no price to estimate from, no JEV score) gets the "unverified" flag, so the panel says its quality is a guess. It still competes on its score (ADR 0013).
6. The top `max_candidates` (5 by default) form the main list; those within `rr_band` of the leader are the round-robin front group (`strategy: round_robin`).

Rankings are recomputed every 30 s, after each catalog sync and after each config change, and cached; requests only read them. Every candidate carries its components, bonuses, flags and a reason string, and rejected models carry the reason (shown in the panel).

## Presets are generated (`internal/app`)
After every probe round `seedPresets` looks at each client whose enabled keys have all been probed. For each kind (text, image, video, tts, stt, music) not yet in the client\'s `seeded` list that has at least one callable catalog model, it creates the default preset once and records the kind. Edited presets are never overwritten and deleted ones never come back. Rankings are computed per client and preset (`rank.Key(client, preset)`), so two clients with different keys get different candidate lists for the same preset name.

## Accounts and keys (`internal/upstream`)
kenari applies rate limits, free quotas and plan budgets **per account**, not per key (https://kenari.id/docs/rate-limits). There is one account, so xellter keeps this state on it: sliding-window rpm, free-model quota counted since 00:00 UTC (tier defaults from kenari: free 5 rpm / 50 per day, topup 10 / 1000, plan 25 / unlimited), plan budget, balance flag. Error codes map to account effects: `free_quota_daily` pauses free models until 00:00 UTC, `free_quota_rpm` for `Retry-After`, `plan_limit_reached` turns plan coverage off, `rate_limit_exceeded` cools the whole account, `402` pauses paid models. Per key: cooldown when the key is rejected (401/403), block of a (key, model) pair after upstream errors with exponential backoff, circuit breaker after consecutive failures, allow list.

**Plan numbers without hardcoding.** `GET /v1/account/quota` and `/v1/account/balance` work only with an *unrestricted* key (restricted keys get 403 `restricted_key_not_allowed`). xellter tries them with every key at start-up and every 5 minutes. When one key is unrestricted, the plan name, the 5 h / weekly / monthly budgets with used and remaining Rupiah and their reset times, and the balance come straight from kenari (our own spend since the last read is added on top). Without an unrestricted key the plan budget is estimated from the request log against the catalog plan prices. The tier is derived from what kenari says: a reported plan means `plan`, a balance above kenari's threshold means `topup`, otherwise `free`.

kenari rejects unknown or disallowed models with `400 … Allowed models: …`, which is free. xellter sends one such probe per key at startup, after config changes and every 6 h, so restricted keys are only used for the models they may call.

## State
| What | Where |
|---|---|
| config (clients, presets, rules, benchmarks) | `xellter.yaml` (0600), history in `data/config-history/` |
| upstream key values | `data/secrets.env` (0600) |
| request log, JEV scores, sessions, video job -> key | `data/xellter.db` (SQLite, WAL) |
| last good catalog | `data/catalog.json` (used when kenari is unreachable) |
| admin login | `data/auth-record.json` (0600), `login-state.json`, `recovery-used.json` |

Counters (rpm, free quota, plan ledger, client spend) are rebuilt from the request log at start-up.

## Failure behaviour
- kenari catalog unreachable: keep serving with the last snapshot (flagged stale in the panel).
- No candidate right now: `503 no_candidates`/`no_capacity` with `Retry-After`; client budget used: `429 budget_exceeded`.
- Streaming failure after the first byte cannot be retried; it is logged as an error.
- Panics in RTK are caught and the original text is sent.

## Calls that belong to no client
Anything that needs an LLM but is not on behalf of a client (today: the JEV judge) uses the dashboard's own key, the optional **account key** (`kenari.account_key_env`, the same key that reads plan and balance), through `Pool.SystemKey()` and `Key.ReserveSystem`. It never borrows a client's key, so a client's quota and logs are not touched. Those calls are logged under the internal client `_system` and excluded from the Usage totals. Without an account key such features report that they need one instead of falling back to a client's key.

## Quality scores from public leaderboards (`internal/bench`)
The Arena (LMArena) leaderboard dataset on Hugging Face needs no key and covers text, text-to-image and text-to-video with human-preference ratings and confidence intervals. xellter downloads the "overall" category when its stored copy is older than a day, keeps the raw rows in SQLite, and re-matches them to the kenari catalog whenever the catalog changes (no network). Names are matched after dropping run settings (`High`, `Max`, `NVFP4`, dates, `Preview`, resolution) and trying aliases written in parentheses; of several entries for one model the most voted wins. A rating becomes `1/(1+10^((median-r)/400))`, using the interval's lower end. Audio and music have no leaderboard in that dataset and keep the price-based estimate. The panel (Settings, Quality scores) shows how many paid models were matched and which were not.
