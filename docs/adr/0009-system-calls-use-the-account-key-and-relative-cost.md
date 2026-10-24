# 0009 — System calls use the account key; cost score is relative to the cheapest candidate

- Status: Accepted
- Date: 2026-10-24
- Deciders: owner (Axell)

## Context
1. The JEV judge picked "the first client key that could call both models", so a client's quota and logs paid for work that belongs to no client.
2. The cost component was log-scaled between the cheapest and the dearest chat model of the whole catalog (684x apart). A 13% price difference moved the score by 0.02, so the "Cheapest" objective hardly preferred the cheaper model: gpt-6-luna (Rp7.69 per typical request) ranked above deepseek-v4-1-flash (Rp6.78) on its quality estimate.

## Decision
- Everything that needs an LLM and belongs to no client uses the dashboard's own key, the optional account key that already reads plan and balance (`Pool.SystemKey`, `Key.ReserveSystem`). Such calls are logged as `_system` and left out of usage totals. No account key means the feature says so; it never falls back to a client key. The per-key JEV setting was removed.
- Cost score = free: 1; paid: cheapest paid candidate's cost / own cost.

## Consequences
- JEV candidates may be models that are not covered by the plan, so judging can spend balance; the daily JEV budget caps it.
- Scores of expensive models drop towards 0 in every preset; their weight is small except under "Cheapest".

## Addendum: scoring accuracy with unrestricted keys
Once the owner lifted the model limits of the kenari keys, a client could call 70+ text models and three flaws showed up: unverified free models led every objective, including Best quality (free/plan bonuses were constant, and unknown quality scored a generous 0.5); the ratio cost score was too steep (expensive plan models fell to 0.00); and a log scale would have been too flat. Decisions:
- Text models without any quality evidence (no configured benchmark, no estimable price, no JEV score) start **on trial** (canary traffic, judged by JEV) and cannot lead until judged or proven by traffic.
- Cost score `1/(1+ln(cost/cheapest non-trial paid candidate))`.
- Free, plan and promo bonuses scale with the preset's cost emphasis (0.25x to 2x).
- Best quality weights became cost 5 / benchmark 40 / JEV 30 / reliability 15 / context 10.
