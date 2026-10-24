# 0003 — Rank models with gates, weighted components and a JEV judge

- Status: Accepted
- Date: 2026-10-24
- Deciders: owner (Axell)

## Context
Clients should use a stable "preset" name while xellter picks the real model automatically: prefer free models, plan-covered models and discounted models, try promising new models, and for non-text modalities order by price. "Cheapest wins" is unsafe: cheap models can be bad (for example an English-only TTS model for Indonesian).

## Decision
Two layers (details in `docs/architecture.md`):
1. **Hard gates**: capabilities, context, allow/deny, sunset, availability.
2. **Weighted score** of cost (30), benchmark (25), JEV evaluation (20), reliability (15) and context (10) for chat presets; media presets weigh cost 70, reliability 20, benchmark 10. Quality (benchmark + JEV + context) must reach `min_quality`, so a free model has to be good enough before being preferred. Bonuses (free, plan-covered, promo) nudge the order.

JEV is kenari's System One decision model: candidates answer golden prompts, JEV scores each answer 0-1 with a typed `noul` question. It is capped by a daily Rupiah budget and disabled until the owner enables it. The judges are an ordered list (`jev.judges`), see ADR 0011.

Benchmarks have no public API. A configured score (0-100) wins; otherwise the quality proxy is the price percentile of the model's **undiscounted** list price (a promo must not make a model look weaker; a `:free` twin borrows its paid sibling). The proxy is a placeholder to be replaced by curated benchmark numbers.

New models (first seen within 14 days) are "on trial": they receive a canary share of traffic (default 5%) and join the main list after enough successful requests; a trial model may not cost more than 3× the leader.

## Alternatives considered
- Static combos only (9router style): simple but needs manual upkeep when prices, promos and free tiers change weekly.
- Cheapest-first without a quality gate: rejected, see above.
- LLM-based routing per request: adds latency and cost on the hot path; rankings are cached and refreshed in the background instead.

## Consequences
- Every decision is explainable in the panel (components, bonuses, rejection reasons).
- Quality of the "benchmark" component is only as good as the configured scores; JEV compensates once enabled.
