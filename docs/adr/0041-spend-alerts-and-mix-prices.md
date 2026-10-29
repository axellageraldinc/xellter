# ADR 0041 — Spend alerts, and prices at the client's own traffic

- Status: Accepted
- Date: 2026-10-29
- Deciders: maintainer
- Amends: ADR 0017 (no budgets), ADR 0012 (the panel shows only what matters)

## Context

A runaway loop in one app costs money for hours before anyone looks at the Usage page, and `daily_budget_idr` was
removed on purpose (ADR 0017): a budget that refuses requests is a routing knob, and a refused request is an outage.
Separately, the model picker shows list prices and one reference request (4,000 tokens in, 70% of it cached, 600 out),
which is not what a given client sends: an agent with long cached prompts and a chat app with short ones rank the same
models differently.

## Decision

1. **A spend alert, never a block.** Every five minutes (and on every Overview load) the request log is read: a client
   whose last hour cost at least `alerts.spend_spike_factor` times its usual hour (the rest of the last seven days) and at
   least `alerts.spend_spike_min_idr` rupiah at list price is reported. Defaults 4 and Rp 50,000. A factor of zero or less
   switches it off. `internal/spend` is pure; the app reads the log.
2. **The panel always shows it** as a warning on the Overview. **A webhook is optional**: with `alerts.webhook_url` set,
   each new alert is POSTed as JSON (client name and two rupiah figures, never a prompt), at most once per client per six
   hours. The "sent" memory is in the process, so a restart can send one again. Empty URL: nothing leaves the machine.
3. **The picker prices an average request of the client's own.** For a chat preset, once the client has 20 answered
   requests with tokens in the last seven days, each model row shows `≈ Rp x/req` (`rank.MixCost`: the client's average
   input, cached and output tokens at the model's price today), and the dialog says what the average is. Before that, only
   the list price is shown. It is a display value; ranking is unchanged.

## Consequences

- Config gains an `alerts` section (documented in the Config tab). Nothing in the panel edits it, per ADR 0012.
- A client that spends nothing normally and starts spending alerts only above the floor, so a new client does not alert
  on its first test requests.
- The averages are over all chat-shaped traffic of the client, not per preset.
