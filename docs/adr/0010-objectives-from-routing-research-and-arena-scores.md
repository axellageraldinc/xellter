# 0010 — Cheapest / Balanced / Best quality as points on the cost-quality frontier, with quality from public leaderboards

- Status: Accepted
- Date: 2026-10-24
- Deciders: owner (Axell)

## Context
The three objectives were hand-picked weights over a quality estimate that, for most models, was a price percentile. With 70+ callable models this produced nonsense: Cheapest and Balanced were identical, weak free models outranked strong paid ones, and Best quality led with free models. We looked for how the literature defines these trade-offs and for a benchmark source that covers every available model and updates itself.

## Research
- **RouteLLM** (Ong et al., ICLR 2025): routing between a strong and a weak model with a threshold on predicted win probability, trained on Arena human-preference data, cuts cost by more than 2x without losing response quality. Lesson: quality is best expressed as a preference/win probability, and a "balanced" setting is a threshold on it, not a price cap.
- **FrugalGPT** (Chen, Zaharia, Zou 2023): prices of comparable models differ by up to two orders of magnitude; trying cheap models first and escalating can match the best model at up to 98% lower cost. Lesson: Cheapest = lowest price among models above a quality floor, with fallback to the next one.
- **RouterBench** (Hu et al., ICML 2024): routers are compared by their cost-quality curve; costs of models of comparable quality vary 2-5x. Lesson: evaluate on the Pareto frontier. A model that another model beats on both cost and quality should never lead.
- **Data sources.** The Arena leaderboard dataset (Hugging Face, no key; text, image, video; rating with confidence interval; refreshed regularly). Artificial Analysis offers a free API key (100 requests a day, internal use with attribution) with an Intelligence Index for 300+ models; it is the best second source but needs a key, so it is a follow-up. BFCL (Berkeley Function-Calling Leaderboard) is the reference for tool calling and a candidate source for the `coding` style presets.

## Decision
- Quality of a model = its Arena win probability against the median model (lower end of the 95% interval), then your own score, then price estimate (see architecture). Refreshed daily.
- A **quality floor** (`min_quality`) defines "good enough", compared with the benchmark score blended with the JEV score when judged. Floors: Cheapest .35, Balanced .50, Best quality .64.
- Objectives differ in what they optimise above the floor: Cheapest 60/10/10/15/5, Balanced 25/35/20/15/5, Best quality 2/55/25/15/3 (cost/benchmark/jev/reliability/context). Free / plan / promo bonuses scale with the objective's cost emphasis and never exceed the free bonus when stacked.
- Failure handling follows FrugalGPT's cascade: the ranking is also the fallback order; an unavailable model sends the request to the next one (see operations).
- Validation harness: `xellter explain` lists list price, quality and score per model; we check on the real catalog that each objective's leaders are not dominated (cheaper and better elsewhere) and that Best quality follows the leaderboard.

## Consequences
- About 49 of ~76 text, 10 of 20 image and 3 of 8 video models are matched today; the rest use the price estimate and, for text, start on trial until judged.
- If the leaderboard cannot be downloaded the stored copy stays in use; with no copy the price estimate applies.
- Lower-bound scoring means a brand-new model with few votes scores low until it is measured.
