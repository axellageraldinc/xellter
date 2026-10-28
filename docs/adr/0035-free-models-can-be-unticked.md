# ADR 0035 — A free or discounted model can be unticked

Date: 2026-10-28

## Status

Accepted. Amends ADR 0022.

## Context

ADR 0022 puts every free and promo model in every pool of its category. A free
model can still be bad (weak, slow, wrong language), and the owner had no way
to keep it out.

## Decision

- A preset carries `excluded`: free or promo model ids the owner unticked.
  `rank.EffectivePool` does not add those to that preset on its own.
- The list is config, so it survives every catalog sync, restart, and a promo
  that ends and later returns. Ids that leave the catalog stay harmless.
- A model in `models` is in the pool whatever `excluded` says; saving drops
  such ids from `excluded`.
- The model picker shows automatic models ticked. Unticking one adds it to
  `excluded`, ticking it again removes it.
- The panel lists the excluded models that would otherwise be added under
  "N free or discounted models added automatically".
