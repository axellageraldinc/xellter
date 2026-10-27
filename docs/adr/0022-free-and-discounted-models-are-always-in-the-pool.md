# ADR 0022 — Free and discounted models are always in the pool

Date: 2026-10-27

## Status

Accepted. Amends ADR 0017 and ADR 0019.

## Context

ADR 0019 made the pool explicit: a model is routed only because the owner
ticked it. That is the right default for a paid model — the pool is a budget
decision.

It is the wrong default for a free model and for a model on offer. A free
model costs nothing; a model with a 90% discount costs a tenth of its price
for as long as the promo runs. Both are worth serving, and both are worth
serving *now*, because the reason to serve them (the price) has a shelf life.

The catalog already knows both: `Model.Free` and `Model.Promo` (with
`EndsAt`). `Model.PromoActive(now, …)` already answers "is the promo running
right now".

## Decision

**Every free model and every model whose promo is running is in the pool of
every preset of that category, whether the owner ticked it or not.**

- The added models are the ones the client's own keys may call, and the ones
  that are usable: a subscriber-only model without a plan, and a model that
  sunsets within `routing.avoid_sunset_days`, are left out.
- The pool follows the catalog, not a snapshot of it. A promo that ends, or a
  model kenari drops, is out of every pool at the next ranking.
- The optimization still orders the pool. The rule adds models to it; it never
  takes one out.
- A free or discounted model is never left out for want of quality evidence,
  and never for a below-median score. It is served last instead. `Best Quality`
  keeps its evidence rule for the paid models.
- The owner's own models keep their places. A free model does not push them
  out of the list; it joins the reserve, and the router serves it when the
  models above it are evicted.
- The panel shows the added models in their own list, so it is always clear
  which models the owner picked and which ones xellter put there.

## Consequences

- The pool is no longer only what the owner ticked, so "a preset is an
  explicit pool" now reads: the owner's models, plus the free and discounted
  ones.
- The effective pool is computed at ranking time and cached by the router, so
  the request path never recomputes it.
- A client may name an auto-added model directly, because the model is in one
  of its presets.
- With the current catalog this adds 18 chat models and 8 System One models to
  the pools that have those categories. The `Cheapest` optimization serves the
  free half first, as it always did.
