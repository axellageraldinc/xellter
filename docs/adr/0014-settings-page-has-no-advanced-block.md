# 0014 — The Settings page has no Advanced block and no extra sections

- Status: Accepted
- Date: 2026-10-25
- Deciders: owner (Axell)

## Context
ADR 0012 shrank the panel's client and preset surfaces, but the Settings page kept the shape it
had before: three cards, each with a collapsible "Advanced" block of raw knobs, plus two more
cards below them ("Quality scores" and "Reliability and access").

The "Advanced" blocks were a way of hiding knobs without deciding about them. They held ten
settings across `rules`, `rtk` and `jev` that the owner never touched and never would: a bonus
weight, two look-ahead windows, the RTK size thresholds, the judge threshold, the judge interval
and the judge's golden prompts. Opening a collapsible block to find ten number inputs is not a
better experience than not showing them at all.

The two cards below were worse. "Quality scores" re-implemented the Arena leaderboard, which
xellter already downloads and applies on its own, and offered a hand-curated score table on top
of it. "Reliability and access" hid every operational setting (retries, cooldowns, breaker, panel
hosts, session length, log retention, price sync) behind a single "Advanced" summary, so the card
was almost entirely empty for anyone who did not open it.

## Decision
- **The "Advanced" block is gone.** No section on the Settings page has one.
- **Smart routing** shows `rules.sticky_minutes` and `rules.avoid_sunset_days`. The other `rules`
  knobs keep the backend default.
- **Token saver (RTK)** shows the on/off switch and the five filters. `min_chars`, `max_chars` and
  `diff_context` keep the backend default.
- **Quality judge (JEV)** shows the on/off switch, the judge order and the daily budget.
  `pass_threshold`, `every_hours` and `prompts` keep the backend default.
- **"Quality scores" and "Reliability and access" are removed from the panel.** The leaderboard
  keeps working in the background exactly as before; `benchmarks.*` and `resilience.*` stay in the
  config file. The admin API keeps its `bench` and `bench/refresh` endpoints.
- **"Sign out everywhere"** moves to the sidebar, next to "Sign out", so removing the card does
  not remove the action.
- **The hidden knobs keep the backend default**, which the config already does: a value that the
  config does not name falls back to `config.Default()`. A value that the config does name still
  wins. `internal/config/config_test.go` pins this behaviour.
- **`PUT /admin/api/config` refuses a body with both `config` and `yaml`.** A `GET /config`
  response carries both; sending it back unchanged used to apply the old yaml and silently drop
  the patched config while still answering `ok`. It now answers 400 instead.
- **The skill (`skills/xellter/SKILL.md`) lists every hidden knob, its default and how to change
  it**, so an AI asked to "make it prefer free models" changes the right key instead of adding a
  panel control.

## Consequences
- The Settings page is three cards: Smart routing, Token saver (RTK), Quality judge (JEV).
- Every value the owner might want to change is still reachable, through the Config tab or
  `PUT /admin/api/config`; the skill documents both.
- The panel no longer shows a benchmark score table. The Arena leaderboard is still downloaded
  daily and still decides the `benchmark` component of the score, and the JEV card still shows
  the judge's own scores.
- The panel no longer shows the retry, cooldown, breaker, panel-host, session, log-retention and
  price-sync settings. They keep their defaults and are still in the config file.
- An AI that changes xellter's configuration must read the skill first; the skill is the map of
  what the panel shows and what it does not.
