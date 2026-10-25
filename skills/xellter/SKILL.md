---
name: xellter
description: "Use when changing xellter's configuration: clients, kenari keys, presets, ranking, rules, JEV, RTK, or the admin panel."
version: 1.0.0
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
  see ADR 0012).

## The one rule that decides most questions

**The panel only shows the knobs that matter; everything else is detected or defaulted.**
If a value is not in the panel, it is either detected by xellter (account, plan, tier, limits, the
models a key may call) or it keeps the backend default. Do not add a control to the panel to make a
value changeable; change the value in the config file instead.

## What the panel can change

| Where | What | Notes |
|---|---|---|
| Clients -> a client | Kenari key: add, rename, replace value, remove | A key has a name and nothing else |
| Clients -> a client | Preset: "Optimise for" (Cheapest / Balanced / Best quality) | Sets `weights` and `min_quality` together |
| Clients -> a client | Preset: tie-break strategy | `priority` (always the best) or `round_robin` (take turns) |
| Clients -> a client | Preset: tool calling / image input requirement | Chat presets only |
| Clients -> a client | Add / delete a preset | Names are fixed: text, image, video, tts, stt, music |
| Settings | Rules, RTK, JEV, benchmark scores | Global |
| Config file | The whole YAML, with history and rollback | The escape hatch |

## What the panel cannot change (edit the config file)

`clients[].daily_budget_idr`, `clients[].rtk`, `clients[].passthrough`, `clients[].enabled`,
`clients[].note`, `presets[].mode` / `models` / `min_quality` / `max_candidates` / `rr_band` /
`weights` / `cost_profile` / `description`, `rules.*`, `resilience.*`, `jev.*`, `rtk.*`,
`benchmarks.*`.

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
| Preference for free / plan / discounted models | `rules.free_bonus`, `plan_bonus`, `promo_bonus_max` | .10, .08, .05 |
| How long a session stays on one model | `rules.sticky_minutes` | 30 |
| Models to try per request | `resilience.max_attempts` | 4 |
| Quality judge | `jev.enabled`, `jev.judges` | off, `[jev-1-13:free]` |
| Judge budget per day | `jev.daily_budget_idr` | 1000 |
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
