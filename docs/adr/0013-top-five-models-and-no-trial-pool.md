# 0013 — The top five models serve; there is no trial pool

- Status: Accepted
- Date: 2026-10-24
- Deciders: owner (Axell)

## Context
The ranking engine produced two lists. The main list held the best `max_candidates` models
(4 by default). Everything else that was *new* or *unverified* went into a separate trial pool
and only received a share of the traffic (`canary_percent`, 100% in the live config), so a model
had to earn its place by being judged or by serving well.

In practice the trial pool did the opposite of what it was meant to do. A trial model was kept
out of the main list because its quality was unknown, but the score it received was computed
exactly like everybody else's. The live ranking for `babu/text` showed the effect:

```
 1. minimax-m3                 score 1.123
 2. agnes-2-5-flash:free       score 1.101
 3. agnes-3-0-flash:free       score 1.090
 4. qwen3-8-omni-flash:free    score 1.070
 T  ornith-1-5-122b:free       score 1.143   <- better than everything above, on trial
 T  minimax-m3-1-flash         score 1.135   <- better than everything above, on trial
 T  nemotron-3-nano-omni...    score 1.105   <- better than everything above, on trial
```

The three best-scoring models in the catalog were locked out of the main list and only got in
through the back door, because they had been added to kenari within the last 14 days. The trial
pool was a second, hidden ranking that disagreed with the visible one, and the "on trial" state
was decided by a date rather than by a quality.

The owner also wanted more room: five models instead of four.

## Decision
- **The top five models serve.** `max_candidates` defaults to 5 for every preset kind, and the
  models outside the top five are rejected with the reason "outside top 5".
- **There is no trial pool.** A model is no longer held back because it is new or because its
  quality is unknown. The score decides; the top five by score serve.
- **The "unverified" flag stays, but only as information.** A text model with no quality evidence
  at all (no configured benchmark, no leaderboard score, no price to estimate from, no JEV score)
  is still marked "unverified", so the panel says that the quality behind it is a guess. It does
  not keep the model out of the list.
- **The trial machinery is removed**, not left behind as a switch: `rules.new_model_days`,
  `rules.canary_percent`, `rules.canary_min_requests` and `rules.canary_min_success`, the
  `canary` flag in the request log, the `seen` table with the first-seen dates, the `/v1/models/changes`
  feed and the canary pick in the router are all gone. The cost score now takes the cheapest paid
  model in the pool as its reference, instead of the cheapest paid model that was not on trial.

## Consequences
- A model that kenari adds today can serve today, in the position its score gives it. The first
  day of a model's life is no longer different from any other day.
- The panel has one list per preset: the five models that serve, and the reason every other model
  was left out. The "On trial" section and the "trial" badge in the request log are gone.
- The JEV judge now only judges the models in the main list, which is where a score can change
  the outcome.
- The `seen` table and the `canary` column are dropped from the SQLite file on the first start
  after the upgrade.
- A model with no quality evidence may still lead on a "Cheapest" preset, because its cost is
  known and its quality is only a guess. The "unverified" flag says so. Raise `min_quality` or
  set a benchmark for the models that matter if that is not wanted.
