---
name: xellter
description: "Use when operating xellter: adding a client, a kenari key or a preset, choosing an optimization, picking models, reading the panel's Serving now and eviction list, or changing a routing knob."
version: 2.0.0
author: Hermes Agent
license: MIT
platforms: [linux]
metadata:
  hermes:
    tags: [xellter, llm-gateway, kenari, admin-panel, presets, routing, eviction]
---

# xellter — the LLM gateway in front of kenari.id

xellter is one OpenAI/Anthropic-compatible endpoint in front of kenari.id. It owns the clients (the
apps that call it), their kenari keys and their presets. An app asks for a preset name (`text`,
`image`, `video`, `tts`, `stt`, `music`, `systemone`, or a name of the owner's own) as the model;
xellter picks a real model from that preset's pool for every request.

Repo: `~/apps/xellter`. Docs: `docs/architecture.md`, `docs/configuration.md`, `docs/adr/` (read the
newest ADR, 0017–0023, before changing the design). Accepted design: `docs/rewrite-plan.md`.

The one rule that decides most questions: **a preset is a pool of models plus one optimization, and
a preset is named by the owner.** Nothing is generated, nothing is seeded. A paid model is used only
because it is ticked; a free or discounted model of the category is always in the pool, and leaves
it again when its promo ends (ADR 0022).

## When to Use

- A client (an app, an agent, …) needs an xellter key, a kenari key, or a preset.
- The owner wants a preset cheaper, stronger, or wants a different model to answer.
- Something is not routed as expected: the wrong model answers, a request 503s, a model is never used,
  a model keeps disappearing.
- A routing knob needs changing (`routing.*`).
- An old config must be migrated.

## Add a client

Panel -> **Clients** -> **New client**: name, purpose, optional kenari key. The xellter key is shown
once — copy it then; only its sha256 is stored. Or:

```bash
./bin/xellter client add -name demo -purpose coding -out data/demo.key
```

Purpose picks the text scoring sources and nothing else:

| Purpose | Scored on |
|---|---|
| General | Arena text |
| Coding | SWE-bench Verified 50 %, Arena webdev 30 %, Aider polyglot 20 % |
| Agentic | Arena agent |
| Vision & documents | Arena vision 50 %, Arena document 50 % |
| Bulk | cost only; Best Quality is not offered |

A purpose reorders the pool; it never changes which models are in it. It applies to the **text**
preset only.

## Add a kenari key to a client

Panel -> the client -> **Add key**: a name and the key value. A key has a name and nothing else —
xellter probes kenari to learn which models it may call, and reads account, plan, tier and limits by
itself. Or:

```bash
echo 'kn-...' | ./bin/xellter key add -client demo -name main
```

The value goes to `data/secrets.env` (0600); the YAML keeps only `key_env`. Rename moves the secret
with the name. Replace changes the value without touching the config.

Add the optional full-access **account key** on Overview (or `kenari.account_key_env`). It only reads
plan windows and balance; it never serves a request.

## Create a preset

Panel -> the client -> **Add preset**: give it a name and pick a category. It starts empty and
Balanced. The name is the model the app asks for, so it is yours to choose (ADR 0021).

| Category | Suggested name | Serves |
|---|---|---|
| chat | `text` | `/v1/chat/completions`, `/v1/responses`, `/v1/messages` |
| images | `image` | `/v1/images/*` |
| videos | `video` | `/v1/videos` |
| audio_speech | `tts` | `/v1/audio/speech` |
| audio_transcription | `stt` | `/v1/audio/transcriptions` |
| music | `music` | `/v1/music` |
| systemone | `systemone` | kenari's System One models; no API path is mapped to this category yet, so it cannot serve `/v1/chat/completions` |

**One preset per category.** The category is the limit, not the name: a second text preset cannot
be expressed and validation refuses it. "Add preset" only offers the categories the client does not
have yet.

**Renaming** a preset changes the model name the app asks for. The apps must be updated. The preset
keeps its category, its optimization and its pool.

## The free and discounted models

Every free model and every model whose promo is running is in the pool of every preset of that
category, whether you tick it or not (ADR 0022). The panel shows them in their own list on the
preset card, and they leave the pool on their own: a promo that ends, or a model kenari drops, is
out at the next ranking. A free model is never left out for want of a quality score; it is served
last instead.

## Choose an optimization

| Optimization | Use when | Ordering |
|---|---|---|
| Cheapest | cost is the only thing that matters | reference cost ascending; `top_models`/2 free + `top_models`/2 plan-covered (5 + 5 by default), each half by its own cost |
| Balanced | the default | 50 % cost score, 50 % quality |
| Best Quality | quality is the only thing that matters | quality only, with a floor at the pool median; models with no evidence are left out |

Notes that save time:

- Cheapest is **cost only**. A cheap model with no quality evidence will win it. That is intended.
- Best Quality never lists a model below the pool's median, and never lists an `unranked` model. A
  pool of models with no public score produces an empty list — pick another optimization or other
  models.
- Quality is filled for **text** presets only: every purpose maps to chat arenas. On image, video,
  TTS, STT, music and System One presets every model is `unranked`, so Balanced behaves like cost
  order and Best Quality serves nothing. Use Cheapest or Balanced there.
- Balanced is the only one that mixes the two, and it is what a new preset gets.

## Pick models

Panel -> the preset -> **Choose models**. The picker is grouped by vendor (kenari's `owned_by`), and
the family checkbox selects every model of that vendor in that category. A row reads:

```
claude-opus-5        plan   1.0M ctx   text+image   tools   In Rp100k, Out Rp500k, CR Rp40k, CW Rp100k
```

Name, billing class, context size, modalities, tool calling, and the price line in IDR. Free and
plan-covered rows show list prices greyed so the pool can be compared; a price kenari does not
publish is `—`. A model no key of the client may call says so and is not callable.

Rules:

- Everything ticked is in the pool; the optimization only orders it.
- A model that is not in the pool cannot be called by name either. Being in a pool **is** the allow
  list.
- A `:free` twin and its paid sibling are separate ids; both can be in the pool.
- System One models are their own category and never enter a text pool.

## Read the panel

**Serving now** — the ranked list for the preset, in order: rank, model, billing class, score, and a
one-line reason ("score 0.612 (cost 532.00, quality 0.83, plan) from text — balanced: 50% cost, 50%
quality"). The order decides who goes first in the round-robin and who serves after an eviction.
Every model in the list is used in turn; a sticky preset keeps a session on the model it is pinned to.

Models that tie on price or score (all the free ones) are ordered by how fast they answer: the median wait for
the first token over the last seven days, raised by the failure rate; a model nobody measured counts as average.
The reason column shows the wait that decided ("first token ~3.9s").

**Out for now** — models taken out of this preset, each with the reason (with its numbers) and the
moment it returns. **This preset of this client only.** Three rules (ADR 0020, 0029):

- *errors*: `evict_errors` failures in a row inside `evict_window_minutes` — out for `evict_cooldown_minutes`;
- *unreliable*: `fail_percent` of its last `health_min_requests`+ requests failed — out for `quarantine_minutes`;
- *slow*: the median wait for the first token is above `slow_first_token_s` — out for `quarantine_minutes`.

A model that comes back is judged afresh on half as many requests; if it is bad again the time out
doubles (up to `evict_cooldown_minutes`). A preset is never emptied: the last two models stay in. A
session pinned to a model that goes out moves on the next request. The models that are out are saved,
so a restart does not forgive them.

**Last 24 h** (a column of Serving now) — per model: the median wait for the first token, the share of
failed requests and the number of requests; an `out` badge marks a model that is out. **Taken out
before** lists earlier events with their reason. Use them to decide whether a model belongs in the pool.

**Left out** — every model in the pool that is not serving, grouped by reason: not in the kenari
catalog, wrong endpoint, subscriber-only without a plan, sunsets soon, no key may call it, no quality
evidence, below the pool median, outside the top N.

**Rankings computed X ago** — the last rebuild. Recalculate rebuilds every ranking of every client
and answers immediately; the "Rankings" line then shows the new moment.

**Look up a session** — paste a session id and see the preset, the live pin per preset, requests,
errors, tokens and cost. This is the same data `GET /v1/session` returns to the app.

## The `routing` knobs

All six live in `xellter.yaml` under `routing`. The panel does not edit them (it only displays the
refresh interval on a client page), so a change is a config edit or a `PUT /api/config`.

| Knob | Default | Change it when |
|---|---|---|
| `rank_refresh_minutes` | 5 | the catalog or the leaderboards move faster than the order does. `0` turns the timer off (rankings still rebuild on a sync, a config change and Recalculate) |
| `top_models` | 10 | the pool is large and only a few models should serve; lower it to concentrate traffic, raise it to spread it (1..50) |
| `evict_errors` | 3 | a provider is flaky and you want it out sooner (2) or want more tolerance (5) |
| `evict_window_minutes` | 60 | the failures are spread over a longer period than an hour |
| `evict_cooldown_minutes` | 720 | a model should come back sooner than 12 h, or stay out longer |
| `avoid_sunset_days` | 3 | you want to stop using models that retire earlier (5) or keep them until the end (0) |
| `first_token_timeout_s` | 15 | a streamed answer stays silent this long (+1 s per 10k prompt tokens) before the next model is tried; `-1` is off |
| `slow_first_token_s` | 10 | replies are slow: lower it to take slow models out sooner; raise it if a client sends huge prompts that are slow everywhere |
| `fail_percent` | 40 | a model fails this share of its requests before it is taken out |
| `health_min_requests` | 6 | requests needed before a model is judged |
| `quarantine_minutes` | 30 | first time a slow or unreliable model is out; doubles on each repeat |
| `repin_first_token_s` | 15 | a sticky session leaves a model that is this slow for 2 of its last 3 requests |

`retry_once` is not a knob: a retriable failure (5xx, timeout, 429, network) always retries the same
model once, then moves to the next model.

## Sticky session (per preset)

Whether a session keeps one model for its whole life is a **preset** setting, not a routing knob
(ADR 0025). The old `routing.sticky_session` is refused on load.

**Panel** -> the client -> the preset card -> **Sticky session**.

| Setting | Meaning |
|---|---|
| on (default) | the first request picks a model and the session stays on it, so the provider's prompt cache stays warm |
| off | every turn is routed again, so the traffic spreads over the whole pool |

- It is per preset: one client can hold a sticky chat preset and a roaming bulk preset.
- A preset that says nothing in the config sticks, so an old file keeps its behaviour.
- A roaming preset keeps no pin, so `/v1/session` reports the last model it served, not a pinned one.
- Turn it off for bulk traffic, where one model behind one key is a rate limit waiting to happen. Keep
  it on for chat, where the conversation changes model halfway through otherwise.

Apply a change: the panel applies at once; a hand edit needs a restart
(`systemctl --user restart xellter` — never without the owner's permission).

```bash
./bin/xellter check -config xellter.yaml     # validate
./bin/xellter explain -client demo -preset text   # print the ranking and every reason
```

## Endpoints

| Method and path | Use |
|---|---|
| `POST /v1/chat/completions`, `/v1/responses`, `/v1/messages` | chat; no translation, `model` is rewritten and the body forwarded |
| `POST /v1/images/*`, `/v1/audio/speech`, `/v1/audio/transcriptions`, `/v1/videos`, `/v1/music` | media |
| `GET /v1/videos/{id}` | a video job, always through the key that created it |
| `GET /v1/models`, `/v1/models/{id}` | the client's presets plus the real models in its pools |
| `GET /v1/session` | the pinned model (or the last model served, on a roaming preset), vendor, billing, context, tokens and cost of one session |
| `GET /v1/usage?range=24h\|30d` | per model and billing kind, in kenari's shape |
| `GET /v1/usage/keys` | per kenari key name (the key that actually served the request) |
| `GET /v1/usage/models?preset=` | per preset and model |
| `GET /v1/account/balance`, `/v1/account/quota` | kenari's own numbers |
| `GET /healthz` | catalog size, age, degraded |

Auth is `Authorization: Bearer <xellter key>` or `x-api-key: <xellter key>`.

```bash
# what is this conversation using?
curl -s "$BASE/v1/session?session=$SID" -H "Authorization: Bearer $XL_KEY"

# where did the traffic go?
curl -s "$BASE/v1/usage/keys?range=24h" -H "Authorization: Bearer $XL_KEY"
curl -s "$BASE/v1/usage/models?range=24h&preset=text" -H "Authorization: Bearer $XL_KEY"
```

Session ids come from `X-Xellter-Session`, `X-Session-Id`, `X-Claude-Code-Session-Id`, the body's
`prompt_cache_key` or `user`, or a hash of the first user message (ADR 0030). `GET /v1/session` answers 404
`no_session` until the session has traffic, so a client can tell "not started" from "unknown".

## Get the current model

The model xellter actually routed to is in the request log, not the config. Two ways to check:

**API (fastest, session-scoped):**
```bash
# Find your session id from X-Xellter-Session header or from the DB
# Then query the session endpoint
curl -s "$BASE/v1/session?session=$SID" -H "Authorization: Bearer $XL_KEY"
```
Returns pinned model, vendor, billing, context, tokens and cost for that session. Answers 404 `no_session` until traffic exists.

**DB (full history, any client):**
```bash
sqlite3 ~/apps/xellter/data/xellter.db \
  "SELECT session, model, client, ts FROM requests WHERE ok=1 ORDER BY ts DESC LIMIT 20;"
```
Or via Python for formatted output. Session IDs come from `X-Xellter-Session`, `X-Session-Id`, `X-Claude-Code-Session-Id`, the body's `prompt_cache_key` or `user`, or a hash of the first user message.

## Migrate an old config

```bash
./bin/xellter migrate -config xellter.yaml           # print the new file + notes
./bin/xellter migrate -config xellter.yaml -write    # old file kept as xellter.yaml.pre-rewrite
```

Keeps server, kenari, clients, keys, hashes and notes; purpose becomes `general`, optimization
`balanced`, and each preset keeps its name and model list. The old global `routing.sticky_session`
becomes the per-preset `sticky` flag. The notes name every preset left with an empty pool and every
preset dropped for not being one of the seven fixed names. Secrets are untouched.

## Gotchas


- A config that still carries `rules`, `resilience`, `rtk`, `benchmarks` or a retired preset key
  (`weights`, `require`, `min_quality`, `max_candidates`, `strategy`, `rr_band`, `mode`,
  `cost_profile`, `description`) **fails to load** with a message naming ADR 0017. That is not a bug:
  run `xellter migrate`, or delete the section.
- A config that still carries `routing.sticky_session` **fails to load** with a message naming
  ADR 0025. Stickiness is a per-preset toggle now; run `xellter migrate`, which carries the old
  switch over to the presets.
- `passthrough`, `daily_budget_idr` and `seeded` are silently ignored — they no longer exist.
- A new client has no presets and nothing to route to until you create one and tick models.
- RTK is always on and only touches tool outputs. There is no switch and no header.
- The xellter key of a client is shown once; rotating it stops the old one at once.
- Keys of one kenari account share its limits, and there is only one account.
- The request log stores metadata only: never prompts, never responses.
- xellter never asks a model to score another model, and the owner never types a quality number.
  Quality is downloaded from public leaderboards (`GET /api/bench` shows what matched).
- Tests never call the network or a paid API; run `./scripts/ci.sh`.
