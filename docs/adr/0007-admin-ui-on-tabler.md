# 0007 — Admin panel on Tabler, simple by default, details under "Advanced"

- Status: Accepted
- Date: 2026-10-24
- Deciders: owner (Axell)

## Context
The first panel used hand-written CSS and exposed every config field as a form input. The owner found it ugly, unclear and full of settings nobody should set by hand.

## Decision
- Build on an existing design system instead of custom styling: **Tabler** (MIT, Bootstrap-based admin kit), vendored as one CSS file (`internal/admin/ui/vendor/tabler.min.css`, no JavaScript, no CDN, no build step) and embedded in the binary. Dark mode uses its `data-bs-theme`. Only a small `app.css` adds a dialog, toast and chart.
- Layout: vertical sidebar (Overview, Clients, Usage, Settings, Config file), hash routes (`#/clients/babu`), Inter variable font and a Tabler Icons subset vendored next to the CSS.
- Default view of every page shows what matters and a few plain-language controls; everything else sits behind an "Advanced" disclosure.
  - Overview: health banner with actionable warnings, 24 h numbers, live plan budget and balance, which model each preset serves now, key states.
  - Presets: what it serves now, "Optimise for" (cheapest / balanced / best quality, which sets the weights), tie behaviour, required capabilities.
  - Kenari keys: add a key by pasting it; plan is read from kenari or chosen from a list; tier/account/limits are advanced.
  - Clients: table with allowed presets, spend and per-row actions.
  - Usage: totals, chart, breakdown by model / preset / client / key, recent requests.
  - Settings: routing, token saver, quality judge, known quality scores.
- Preset names no longer carry the `xl-` prefix (it only meant "xellter" and confused). Client API keys keep the `xl-` token prefix, which marks them as xellter keys.

## Consequences
- +536 KB in the binary for the CSS; no runtime cost.
- Custom weights still work: the panel shows "Custom" and exposes them under Advanced.
