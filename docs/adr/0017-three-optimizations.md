# 0017 — Three optimizations replace weighted scoring

- Status: Accepted
- Date: 2026-10-26
- Deciders: owner (Axell)
- Supersedes: ADR 0003, ADR 0013, the scoring parts of ADR 0010

## Context
Ranking had grown into a scoring laboratory: five weighted components, per-preset weights, a quality
gate, three bonuses scaled by a cost weight share, a round-robin band and a trial pool. Every knob was
defensible on its own; together they made it impossible to answer "why this model?" without reading the
engine, and impossible for the owner to predict what a config change would do.

The owner asked for a router, not a scoring laboratory: three named choices, one of which a preset picks.

## Decision
A preset is **an explicit pool of models plus one optimization**. The pool is what the owner ticked in the
panel; ranking never adds a model and never removes one for being new, cheap, expensive or unknown.

| Optimization | Ordering | The list |
|---|---|---|
| Cheapest | reference cost ascending, nothing else | **five free + five plan-covered**, each half by its own cost; an empty half is topped up from the other, then from pay-as-you-go |
| Balanced | `0.5 · cost score + 0.5 · benchmark` | one ranking, top 10 |
| Best Quality | `benchmark` only, floor at the pool median | one ranking, top 10; models with no evidence excluded |

Two pools for Cheapest, because a free model and a plan-covered model are not comparable on price: the
first costs nothing ever, the second spends plan budget. One ranking would let a cheap plan-covered model
displace a free one, which is the wrong answer for "cheapest".

**Cost is one number.** Every price becomes the price of one reference request per category (chat: 4 000
input tokens, 70 % of them cache reads, 600 output; one image; 5 s of 720p video; 1 000 characters; 60 s;
one song), so input, output and cache read are never compared in isolation. A running promo is already in
the catalog's price, so it is included automatically. Cache write is displayed in the panel but not ranked
on: only 21 of 110 catalog models publish it, so including it would favour those 21.

**Deleted:** `Weights`, `free_bonus`, `plan_bonus`, `promo_bonus_max`, `plan_cost_factor`,
`promo_min_remaining_hours`, `min_quality`, `rr_band`, `max_candidates`, `strategy`, `mode` (auto/combo),
`cost_profile`, `require` gates, and reliability/context as ranking components. A config file that still
carries any of them is refused on load with a message naming ADR 0017, so nobody believes it still works.
`xellter migrate` converts an old file.

## Alternatives considered
- **Keep the weights and hide them.** The panel already hid most of them (ADR 0012, 0014); the behaviour
  was still unpredictable, which is what the owner objected to.
- **One cost pool with a quality floor.** A floor is a second rule wearing the name of the first. The
  owner chose cost-only for Cheapest, and the floor belongs to Best Quality.
- **Rank every model in the catalog instead of an explicit pool.** That is what the old engine did, and it
  meant the owner could not exclude a model for a reason the engine cannot see.

## Consequences
- The config surface drops from 88 knobs to about 30.
- Every candidate carries its cost, quality, billing class and a one-line reason; every model left out
  carries a reason too, so "why is this not serving?" is answerable from the panel.
- The score is a deterministic function of the catalog, the account state and the leaderboard snapshot:
  the same inputs always produce the same order.
