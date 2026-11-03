# ADR 0043 — A daily cap per preset, in rupiah

- Status: Accepted
- Date: 2026-11-03
- Deciders: maintainer
- Amends: ADR 0041 (a spend alert never blocks; a cap the owner sets does), ADR 0017 (no budgets)

## Context

An alert (ADR 0041) tells the owner after the money is spent. The owner wants a ceiling that holds on its own: a preset
that has spent what it was allowed to spend today serves nothing more.

## Decision

1. **`daily_cap_idr` on a preset**, in rupiah. 0 or absent is no limit, which is what every existing preset has.
2. **At the cap the preset refuses.** Before routing, if the preset's cost since local midnight is at or above the cap,
   the request is answered `429 daily_cap_reached` with `Retry-After` set to the seconds until midnight. Nothing is sent
   upstream. Other presets of the same client are unaffected.
3. **One unit.** Every logged cost is in rupiah. A provider that quotes another currency sets `currency` (and, for
   anything but USD, `idr_per_unit`, an estimated rate); its costs are converted when the request is booked. USD
   without `idr_per_unit` uses the catalog's own `usd_idr` rate. IDR (the default, and kenari) is unchanged.
4. **The count** is an in-memory counter per client and preset, filled from the request log on the first use of a day and
   after a restart, and added to as each request finishes.
5. **Set in the panel** (preset card, "Daily cap", empty = no limit), shown with what is spent today.

## Consequences

- The cap is checked before a request and booked after it, so requests in flight when the cap is crossed finish and can
  overshoot it by their own cost. A streamed answer is never cut off halfway.
- The estimate is only as good as the configured rate; the log keeps the converted rupiah figure, not the original.
- Only successful requests carry a cost, so failed requests do not count.
