# 0016 — Scoring is deterministic; no model judges another

- Status: Accepted
- Date: 2026-10-26
- Deciders: maintainer
- Supersedes: the JEV parts of ADR 0003, the account-key/system-call part of ADR 0009 and ADR 0011 in full

## Context
Quality came from two places: a `benchmark` component (configured score, else the public Arena
leaderboard, else a price estimate) and a `jev` component, where kenari's System One model graded
answers that every candidate model gave to a few golden prompts. JEV brought its own machinery: a
daily Rupiah budget, an ordered judge chain with automatic fallbacks, a cool-down when the judge was
down, a `jev` table in SQLite, a Settings card, and a `Pool.SystemKey` / `Key.ReserveSystem` path so
those calls could run on the dashboard's account key instead of a client's.

That cost did not buy a better order. The judge is itself one of the models under test, so the score
was partly a model's opinion of other models: it moved when the judge changed, it was not
reproducible, and a round could spend real balance to shift a ranking that the leaderboard already
decided for most models. The owner asked for a ranking that is fully deterministic and to keep the
scoring algorithm as a separate conversation.

## Decision
Remove model judgement from xellter entirely.

- **Quality is the `benchmark` component only**: your `benchmarks.<model>` score (0-100, scaled to
  0..1) when set, else the Arena leaderboard score mapped onto the catalog id, else the
  price-percentile estimate, else 0.5. The `min_quality` gate compares this number; the
  `jev.pass_threshold` gate is gone.
- **The `jev` weight is gone** from `Weights`. Remaining weights keep their meaning and are still
  relative. A config file that still carries a `jev:` section or a `jev` weight is refused on load
  with a message that names the reason, instead of being silently ignored, so nobody believes the
  judge still runs.
- **Deleted with the judge**: `internal/jev`, the `jev` config section (`enabled`, `judges`,
  `model`, `daily_budget_idr`, `every_hours`, `pass_threshold`, `prompts`), the `jev` SQLite table,
  `Pool.SystemKey`, `Key.ReserveSystem`, the `/v1/models?modality=systemone` catalog fetch, the
  `/admin/api/jev` and `/admin/api/jev/run` endpoints, and the Settings "Quality judge" card.
- **The account key stays**, for what it was introduced for: reading plan quota and balance. With no
  client of its own it still never serves client traffic.
- **Existing state is cleaned on open**: the `jev` table is dropped and the `_system` rows the judge
  wrote to the request log are deleted, so Usage and the model health statistics no longer carry
  calls that no client made.

## Alternatives considered
- **Keep JEV off by default.** It already was off. Leaving the code in place keeps a second,
  non-deterministic quality path that any config edit can switch back on, and keeps the budget,
  cool-down and judge-chain code alive for nobody.
- **Replace JEV with another LLM (a different judge, or several voting).** Same problem with more
  moving parts: still an opinion, still not reproducible, still a per-round cost, and it would need
  the same system-key plumbing that this decision removes.
- **Drop quality scoring to cost + reliability only.** Cheapest would win among equals, which is
  what the leaderboard component exists to prevent (ADR 0010): a cheap model can be bad.
- **Keep the `jev` weight key as a no-op** for config compatibility. A weight that does nothing is
  worse than a validation error: it hides the fact that the score changed.

## Consequences
- The order for a given catalog snapshot, account state, request log window and leaderboard is
  reproducible: same inputs, same ranking. `internal/rank` remains pure and does no I/O.
- Models the leaderboard does not cover (audio, music, most video, unmatched ids) rely on the
  price-percentile estimate, and a text model with no evidence at all is still flagged "unverified"
  and still competes. Curating `benchmarks.<model>` is now the only way to improve their quality
  signal, so that list matters more than before (ADR 0010 follow-up).
- Existing config files that carry a `jev:` section or a `jev` weight are refused on load with a
  clear message; the live `xellter.yaml` was migrated as part of this change. A config sent as JSON
  through the admin API has unknown fields dropped instead (encoding/json ignores them).
- The scoring formula itself is unchanged otherwise; tuning it is a separate, later decision. One
  side effect: the free / plan / promo bonus scale is the preset's cost weight share divided by
  0.30, and the share is now computed over the remaining weights. Balanced (25/35/15/5) therefore
  scales bonuses by 1.04x instead of 1.0x; Cheapest and Best quality stay clamped at 2x and 0.25x.
  Relative weights are unaffected, because the score is a weighted average over the weights that
  are set.
