# 0012 — The panel shows the knobs that matter; everything else is detected

- Status: Accepted
- Date: 2026-10-24
- Deciders: maintainer

## Context
The admin panel had grown a configuration surface that nobody used and nobody wanted:
a key dialog with tier, account, plan, rpm, daily quota, allow list and note; a preset card with
"Models it may use" (a tick list that wrote a `deny` list), a context-size dropdown, and an
"Advanced" block with a dozen raw knobs; a "Client settings" card with the client's enable switch,
daily budget, RTK override, passthrough list and note. Most of it was wrong by default anyway:
the tier was usually wrong (it was configured, while kenari knows the real one), the allow list was
detected anyway, and the context-size gate silently dropped models that were perfectly usable.

The panel is the tool the owner actually touches. Every control it shows is a chance to get
something wrong, and every control it hides is a decision that xellter can make better on its own.

## Decision
- **A kenari key has a name and nothing else.** The account, the plan, the tier, the limits and the
  models a key may call are detected by xellter (probe + quota + balance), never configured. The
  panel can rename a key (the secret moves with it) and replace its value. There is one kenari
  account, so the per-account knobs (`account`, and the limits that kenari applies per account)
  are gone.
- **A preset has no model filters and no context gate.** The models the client's keys may call are
  the models the preset may use. The context size stays only as a soft preference in the score.
- **The panel shows the objective buttons, the tie-break strategy and the capability requirements
  (tool calling, image input).** Everything else on a preset keeps the backend default.
- **No client-level card.** The client-level knobs keep their defaults; the config file still has
  them for hand editing.
- **Anything that must be changed by hand is a config-file edit**, and the panel's Config tab is
  the same file with history.

## Consequences
- The panel cannot set the tier, the rpm, the daily quota, the allow list, a preset's model
  filters, a context floor, the daily budget, the RTK override or the passthrough list. These are
  still in the config file and still work.
- The removed config keys are ignored on load (unknown keys are ignored) and no longer written
  back, so the live config shrinks on the next save.
- A skill (`skills/xellter/SKILL.md`) documents the knobs that remain and how to change them by
  hand or through the admin API.
- The panel is now mostly a status view plus three or four real choices per preset.
