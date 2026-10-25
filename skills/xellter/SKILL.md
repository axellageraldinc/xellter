---
name: xellter
description: "Use when changing xellter's configuration: clients, kenari keys, presets, ranking, rules, JEV, RTK, or the admin panel. Also use when the owner asks to change a value the panel does not show."
version: 1.1.0
author: Hermes Agent
license: MIT
platforms: [linux]
metadata:
  hermes:
    tags: [xellter, llm-gateway, kenari, admin-panel, presets, routing]
---

# xellter — the LLM gateway in front of kenari.id

xellter is one OpenAI/Anthropic-compatible endpoint in front of kenari.id. It owns the clients
(the apps that call it), their kenari keys, and the **presets** it generates for them. An app asks
for a preset name (`text`, `image`, `video`, `tts`, `stt`, `music`) as the model; xellter picks the
best real model behind it for every request.

Repo: `~/apps/xellter`. Docs: `docs/architecture.md`, `docs/configuration.md`, `docs/adr/`.
Decisions live in `docs/adr/` — read the latest ADR before changing the design.

## When to Use

- The owner asks to change how xellter routes: a preset should be cheaper, stronger, or should
  prefer a different model.
- A client (Babu, aixell, polifin, ...) needs a new kenari key, a new xellter key, or a new preset.
- Something is not routed as expected: the wrong model answers, a request 503s, a model is never used.
- The owner wants a value changed that the panel does not show (the panel is deliberately small,
  see ADR 0012 and ADR 0014). **Read this skill before answering: it lists every knob, where it
  lives and how to change it.**

## The one rule that decides most questions

**The panel only shows the knobs that matter; everything else is detected or defaulted.**
If a value is not in the panel, it is either detected by xellter (account, plan, tier, limits, the
models a key may call) or it keeps the backend default. Do not add a control to the panel to make a
value changeable; change the value in the config file instead.

The Settings page has **no "Advanced" block**: Smart routing, Token saver (RTK) and Quality judge
(JEV) each show a few real switches, and everything else is a config-file edit (ADR 0014). The
values that the panel hides still work; they just keep their default until the config names them.

## What the panel can change

| Where | What | Notes |
|---|---|---|
| Clients -> a client | Kenari key: add, rename, replace value, remove | A key has a name and nothing else |
| Clients -> a client | Preset: "Optimise for" (Cheapest / Balanced / Best quality) | Sets `weights` and `min_quality` together |
| Clients -> a client | Preset: tie-break strategy | `priority` (always the best) or `round_robin` (take turns) |
| Clients -> a client | Preset: tool calling / image input requirement | Chat presets only |
| Clients -> a client | Add / delete a preset | Names are fixed: text, image, video, tts, stt, music |
| Settings -> Smart routing | `rules.sticky_minutes`, `rules.avoid_sunset_days` | Global |
| Settings -> Token saver (RTK) | `rtk.enabled` and the five filters (git diff, grep, ls, logs, truncate) | Global |
| Settings -> Quality judge (JEV) | `jev.enabled`, `jev.judges` (the ordered chain), `jev.daily_budget_idr` | Global |
| Overview | Kenari account key (add / replace / remove) | It only reads plan, usage and balance |
| Config file | The whole YAML, with history and rollback | The escape hatch |

There is **no "Advanced" block and no other section on the Settings page** (ADR 0014). Everything
that is not in the table above keeps the backend default or is a config-file edit.

## What the panel cannot change (edit the config file)

`clients[].daily_budget_idr`, `clients[].rtk`, `clients[].passthrough`, `clients[].enabled`,
`clients[].note`, `presets[].mode` / `models` / `min_quality` / `max_candidates` / `rr_band` /
`weights` / `cost_profile` / `description`, `resilience.*`, `server.*`, `kenari.*`,
`benchmarks.*`, and the hidden parts of `rules`, `rtk` and `jev`:

| Hidden knob | Config key | Default |
|---|---|---|
| Preference for free / plan / discounted models | `rules.free_bonus`, `rules.plan_bonus`, `rules.promo_bonus_max` | .10, .08, .05 |
| What a plan-covered request counts for | `rules.plan_cost_factor` | .3 |
| Ignore a discount that ends within (h) | `rules.promo_min_remaining_hours` | 12 |
| Look ahead for price changes (h) | `rules.upcoming_lookahead_hours` | 24 |
| Health window (h) | `rules.stats_window_hours` | 24 |
| Compress only outputs longer than (chars) | `rtk.min_chars` | 2000 |
| Hard cap (chars) | `rtk.max_chars` | 12000 |
| Context lines kept around a diff change | `rtk.diff_context` | 1 |
| Minimum score to be used (0-1) | `jev.pass_threshold` | .6 |
| Re-judge every (hours) | `jev.every_hours` | 24 |
| The judge's test questions | `jev.prompts` | three default questions (one of them Indonesian) |
| Models to try per request | `resilience.max_attempts` | 4 |
| Your own quality scores | `benchmarks.<model>` | from the Arena leaderboard |

The Config tab edits the same file, validates it, keeps a history and can roll back. A hand edit
is therefore the normal way to change these — do not add a panel control for them.

## The values and where they live

| Want | Set | Default |
|---|---|---|
| Cheapest / Balanced / Best quality | panel button on the preset | Balanced (25/35/20/15/5, min_quality .50) |
| Quality floor of a preset | `presets.<name>.min_quality` | .50 chat, 0 media |
| Always the best model vs. take turns | `presets.<name>.strategy` | round_robin (chat), priority (media) |
| How many models to keep | `presets.<name>.max_candidates` | 5 |
| How close counts as equal | `presets.<name>.rr_band` | .04 chat, .03 media |
| How much each factor counts | `presets.<name>.weights` | per objective above |
| The reference request that turns prices into one number | `presets.<name>.cost_profile` | 4000 in / 600 out tokens, 70% cached |
| A model must be able to call tools | `presets.<name>.require.tool_call` | true for chat |
| A model must take images | `presets.<name>.require.vision` | false |
| Keep one conversation on one model | `rules.sticky_minutes` (panel) | 30 |
| Stop using models that end within (days) | `rules.avoid_sunset_days` (panel) | 3 |
| Shrink big tool output | `rtk.enabled` and the filters (panel) | on, all five filters on |
| Quality judge on / off, and which judge answers first | `jev.enabled`, `jev.judges` (panel) | off, `[jev-1-13:free]` |
| Judge budget per day | `jev.daily_budget_idr` (panel) | 1000 |
| Preference for free / plan / discounted models | `rules.free_bonus`, `plan_bonus`, `promo_bonus_max` | .10, .08, .05 |
| Models to try per request | `resilience.max_attempts` | 4 |
| Your own quality scores | `benchmarks.<model>` | from the Arena leaderboard |

Weights are **relative**, not percentages: `cost 25, benchmark 35, jev 20, reliability 15, context 5`
means "benchmark counts 35/25 = 1.4x as much as cost". The context weight is a soft preference
only; there is no context-size gate any more (ADR 0012).

## How to make a change safely

1. Read the current value: `grep` the config (`~/apps/xellter/xellter.yaml`) or read it from the
   admin API (`GET /admin/api/config`).
2. Change it in `xellter.yaml` (or the Config tab), then validate:
   `./bin/xellter check -config xellter.yaml`.
3. Apply it: the panel saves and applies at once; a hand edit needs a restart of the service
   (`systemctl --user restart xellter`). Never stop the service without the owner's permission.
4. Verify: the "Why this order?" button on a preset card shows the ranking and every reason
   behind it; `GET /admin/api/ranking?client=<name>&preset=<preset>` returns the same data.

Tests: `./scripts/ci.sh` (gofmt, vet, tests, JS syntax). Tests never call the network or a paid
API; kenari is faked with `httptest`.

## How to change a knob the panel does not show

This is the normal path for every hidden knob above. The panel's Config tab is the same file with
history and rollback, so there are two ways.

**Through the admin API (no restart, applies at once).** The service listens on `127.0.0.1:8480`.
The `PUT /admin/api/config` endpoint replaces the **whole** config, so always read it first, patch
one field and send the whole thing back:

```sh
# 1. read the current config and keep it in a file
curl -s -H 'X-Xellter: 1' -b cookies.txt http://127.0.0.1:8480/admin/api/config > /tmp/cfg.json

# 2. patch one value (example: prefer free models more) and keep only {config}
jq '{config: (.config | .rules.free_bonus = 0.2)}' /tmp/cfg.json > /tmp/new.json

# 3. save and apply
curl -s -H 'X-Xellter: 1' -H 'Content-Type: application/json' -b cookies.txt \
  -X PUT http://127.0.0.1:8480/admin/api/config --data-binary @/tmp/new.json
```

Two rules that cost time if broken:
- **Send `{config}` only.** The GET response also carries `yaml`; a body with both is refused
  (400 "send {config} or {yaml}, not both"), and a body with only `yaml` applies the old text.
- **Send the whole config, never a fragment.** The endpoint takes the object as the new truth, so
  a body without `clients` would drop every client. Step 1, patch, step 3 with the full object is
  the safe pattern.

A YAML edit is simpler to read: edit `xellter.yaml`, run `./bin/xellter check -config xellter.yaml`,
then restart the service. The file is machine-written, so the comments in it are not preserved.

**Never** add a control to the panel to make a hidden value changeable. If the owner asks for a
value that is not in the panel, change it in the config (or through the API above) and say so.

## What xellter detects by itself (never configure it)

- Which models a key may call: a probe asks kenari for an unknown model and reads the allow list
  from the answer. Restricted keys learn their list; unrestricted keys are unrestricted.
- The kenari account, its plan, its tier and its limits: read from kenari's quota and balance
  endpoints (needs the account key, `kenari.account_key_env`).
- The balance above which kenari lifts the free-tier limits: from the public pricing endpoint.

## Common tasks

**Make a preset cheaper.** Panel -> the client -> the preset -> "Cheapest". If the cheapest models
are not good enough, raise `min_quality` in the config file instead of the "Best quality" button
(the button also drops the benchmark weight, which is not what you want).

**A model is never used.** Check "Why this order?": it is rejected, with the reason given (a
missing capability, a sunset, a quality floor, or "outside top 5"). A model with no quality
evidence is marked "unverified": it still serves, but its quality is a guess.

**A client gets no answers.** "Nothing available right now" means no model of that kind is
callable with the client's keys: the key has no value, is on cooldown, or the catalog has no model
of that kind for it. The Overview alerts name the client and the key.

**Add a client.** Panel -> Clients -> Add client (this prints the xellter key once), then Add key
with the kenari key value. Presets appear a few seconds after the key is probed.

**"Make it prefer free models more."** `rules.free_bonus` (config file; default .10). It is a
score bonus, not a percentage: .20 makes a free model clearly ahead, .30 makes it overwhelming.

**"Stop using a model that is about to be retired."** `rules.avoid_sunset_days` (panel, default 3)
drops a model that ends within that many days. `rules.upcoming_lookahead_hours` (config, default
24) is how far ahead a price change is noticed.

**"Compress more / less."** `rtk.min_chars` (config, default 2000) is the shortest output that
gets compressed, `rtk.max_chars` (config, default 12000) the hard cap, `rtk.diff_context`
(config, default 1) how many context lines survive around a diff change. The on/off switch and
the five filters are in the panel.

**"Judge more often / only accept good models."** `jev.every_hours` (config, default 24) and
`jev.pass_threshold` (config, default .6) are hidden; `jev.enabled`, the judge order and
`jev.daily_budget_idr` are in the panel. A judge that is not free costs money from the daily
limit, so keep free judges first in the chain.

**"Change the questions the judge asks."** `jev.prompts` (config; a list, one question per line).
The default has three questions, one of them in Indonesian.

**"Try more models before failing."** `resilience.max_attempts` (config, default 4), with
`cooldown_base_s`, `cooldown_max_s`, `breaker_failures` and `breaker_open_s` next to it.

**"Let the panel sign me out everywhere."** Sidebar -> "Sign out everywhere".

## Gotchas

- The xellter key of a client is shown **once**; only its hash is stored. Rotating it stops the old
  one at once.
- Preset names are fixed and may not be invented: `text`, `image`, `video`, `tts`, `stt`, `music`.
  A client has each kind at most once.
- A deleted preset is not recreated (the `seeded` list remembers), but it can be added back.
- Keys of one kenari account share its limits, and there is only one account.
- The request log stores metadata only: never prompts, never responses.
- Anything that needs an LLM but belongs to no client (the JEV judge) uses the account key, never
  a client's key; it is logged as client `_system`.
