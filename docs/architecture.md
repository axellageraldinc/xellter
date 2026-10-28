# Architecture

```
client ──(xl-… key)──▶ proxy ──▶ router ──▶ upstream key pool ──▶ kenari.id
                         │          │              ▲
                         │          ├─ rankings ◀───┤ catalog (models, prices, promos, plans)
                         │          │                └ store (leaderboard scores)
                         │          ├─ round-robin ring + session pins + evictions
                         │          └─ one preset = a pool of models + one optimization + sticky or roaming
                         └─ RTK (always on), usage accounting, request log (SQLite, async)
admin panel ──▶ app.Apply(config) ──▶ hot reload of pool, router, proxy
```

The rewrite that produced this shape is [docs/rewrite-plan.md](rewrite-plan.md); the decisions are
ADR [0017](adr/0017-three-optimizations.md), [0018](adr/0018-client-purpose-changes-text-scoring.md),
[0019](adr/0019-explicit-model-pools-and-vendor-families.md),
[0020](adr/0020-round-robin-top-ten-with-per-preset-eviction.md),
[0021](adr/0021-preset-names-are-the-owners.md),
[0022](adr/0022-free-and-discounted-models-are-always-in-the-pool.md),
[0023](adr/0023-the-panel-is-the-site.md),
[0025](adr/0025-sticky-is-a-per-preset-toggle.md),
[0028](adr/0028-the-effort-does-not-choose-the-model.md),
[0029](adr/0029-slow-and-unreliable-models-go-out-of-the-preset.md),
[0030](adr/0030-a-conversation-is-told-apart-by-its-first-user-message.md) and
[0031](adr/0031-the-size-of-a-request-is-estimated-from-its-content.md).

## Components

| Package | Role |
|---|---|
| `internal/config` | YAML schema, defaults, validation, retired-key refusal, history, secrets file, migration |
| `internal/catalog` | kenari catalog + prices + promos + plans -> immutable `Snapshot`; System One on its own endpoint |
| `internal/store` | SQLite (modernc, no CGO): request log, usage aggregates, leaderboard rows, admin sessions, video job -> key, learned effort refusals, model events |
| `internal/upstream` | per-key and per-account limits: rpm, free quota, plan ledger, cooldown, breaker, learned allow list |
| `internal/rank` | pure ranking of one preset: pool -> rejections -> cost/quality -> top list, with reasons |
| `internal/route` | rankings cache, effective pools, round-robin ring, session pin (saved across restarts), per-preset eviction, model health (slow / unreliable), key ordering |
| `internal/effort` | reads the reasoning effort a request asks for, and remembers the models that refuse it (served without the field), saved across restarts. It does not choose models (ADR 0028) |
| `internal/proxy` | data plane: auth, body rewrite, streaming relay, usage accounting, read-only endpoints |
| `internal/rtk` | tool-output compression, always on |
| `internal/bench` | public leaderboards -> one 0..1 value per model and arena |
| `internal/auth` | admin login: scrypt + TOTP + recovery codes |
| `internal/admin` | the panel: `/api` JSON API + the embedded React app (built by Vite, no CDN) |
| `internal/app` | wiring, `Apply` hot reload, background loops |
| `web` | the panel's source: React, Vite, Tailwind, shadcn/ui; built into `internal/admin/ui/dist` |

## Catalog (`internal/catalog`)

One `Snapshot` holds every model kenari lists, the plans, the free-tier limits and the USD/IDR rate.
Prices are IDR per 1M tokens; media models carry `Lines` (per image, second, 1k characters, song).
A running promo is already inside the listed price; `Effective(now, 0)` applies promo expiry and any
scheduled change. Cache write is kept as a display value (`0` = kenari publishes none, shown as `—`).

**System One is the sixth category.** Its models are not on the main list: they are fetched from
`/v1/models?modality=systemone`, marked with endpoint `systemone`, priced like any other model, and
never mixed into a chat pool. A systemone preset is served by `POST /v1/systemone` and by the chat
paths (kenari serves System One through the OpenAI-shaped endpoint too, `route.servesCategory`); it is
refused on the media paths, and `/v1/systemone` refuses every other category.

Refresh loops:

| Loop | Interval |
|---|---|
| catalog | `kenari.sync_minutes` (10) and at start |
| plan quota + balance | 5 minutes |
| key probe (which models a key may call) | 6 hours, at start, and after a config change |
| leaderboards | at start when older than 24 h, then re-checked every 6 h |
| rank rebuild | `routing.rank_refresh_minutes` (5), after every catalog sync, after every config change, and on demand |
| request-log purge | 6 hours (`server.log_retention_days`) |

A failed catalog fetch keeps the last snapshot and marks it degraded (`/healthz` reports it).

## Configuration and hot reload (`internal/app`)

`xellter.yaml` is the single source of truth. `app.Apply` validates, saves (keeping a history copy
under `<data_dir>/config-history`), then swaps the config into the pool, the proxy and the router;
the router recomputes every ranking. Nothing mutates a published `*config.Config`.

A config file that still carries a retired section (`rules`, `resilience`, `rtk`, `benchmarks`) or a
retired preset key (`weights`, `require`, `min_quality`, `max_candidates`, `strategy`, `rr_band`,
`mode`, `cost_profile`, `description`) is **refused on load** with a message naming ADR 0017 —
otherwise an ignored key would look like it still works. `xellter migrate` converts an old file.

## Request flow (`internal/proxy`)

1. **Authenticate.** `Authorization: Bearer …` or `x-api-key` -> sha256 -> client. Unknown 401,
   disabled 403. The key may also be an Anthropic-style `x-api-key`.
2. **Read the body** (bounded by `server.max_body_mb`), find `model` (JSON or multipart).
3. **Session id.** `X-Xellter-Session`, `X-Session-Id`, `X-Claude-Code-Session-Id`, then the body's
   `prompt_cache_key` or `user`, then a hash of the first user message of `messages` (ADR 0030; the
   system prompt is left out because every conversation of an agent shares it).
4. **`route.Plan`** turns the requested name into attempts:
   - a preset name (`text`, `image`, …) -> the ranking of that client and preset; the path must serve
     that category (`/v1/images/*` cannot be asked to serve `text`);
   - a real model id -> allowed only when it is in one of the client's own preset pools (ADR 0019);
   - for each model in ring order, the client's usable keys: free or plan-covered first, then
     pay-as-you-go; equal keys rotate.
5. **RTK** rewrites tool outputs in chat bodies (`messages[].role=tool`, Anthropic `tool_result`
   blocks, Responses `function_call_output`). No switch, no header: it is always on and only
   compresses tool results, never user or assistant text. Failures keep the original text.
6. **Attempts.** For each `(model, key)`: reserve (rpm, free quota, plan window), rewrite `model`,
   send with the client's headers. For a streamed chat request the first bytes of the answer are read
   **before** the response is committed to the caller: if nothing arrives within
   `routing.first_token_timeout_s` (+1 s per 10k prompt tokens, an estimate: ADR 0031) the request is cancelled and the next model
   is tried (never for the last model that can be tried). Then streaming bodies are copied through in
   16 KB chunks with a flush each; only the head and tail are kept to read `usage`.
7. **Failure.** `Key.Failure` classifies the status and applies its account/key effect; a retriable
   failure retries **the same model on the same key once**, then the model is abandoned for this
   request (its other keys are skipped). A non-retriable error abandons the model at once.
8. **Eviction.** Every failed attempt on a preset request is recorded per `(client, preset, model)`;
   the third failure inside the window evicts the model from that preset and drops any session pin
   pointing at it. A success clears the record, so the rule is three failures in a row, not three ever.
   **Health** (ADR 0029): every attempt also feeds a per-`(client, preset, model)` record of the last
   hour (first-token wait, success). Once it holds `health_min_requests` requests, a median first token above
   `slow_first_token_s`, or `fail_percent` failed requests, takes the model out for `quarantine_minutes`
   (doubling on each repeat, judged on half as many requests when it returns). A preset is never emptied.
   The ranking also reports a **reserve**: the pool members the list size pushed out, in the
   optimization's own order. They sit at the end of the ring, so an evicted model is replaced by the next
   best one instead of shrinking the list.
9. **Success.** Pin the session to the model (a preset that does not stick keeps no pin, ADR 0025; a
   session whose pinned model was slow for two of its last three requests drops the pin and avoids that
   model, ADR 0029), stream the response, add `X-Xellter-Model` and `X-Xellter-Attempt`, compute the cost at catalog
   prices, log metadata (async) and update the account ledger and the client's spend counter.

`X-Xellter-Attempts` is set on the error response when every attempt failed. The upstream's own error
body and status are relayed when one is available.

### RTK (`internal/rtk`)

Always on, no switch, no header, no config key. It runs on chat bodies only (`plan.Modality ==
"chat"`), and only on tool results: `messages[].role=tool`/`function`, Anthropic `tool_result`
blocks, Responses `function_call_output`. User and assistant text is never touched. Text under 2 000
characters is left alone; anything a filter shrinks is capped at 12 000 characters.

| Filter | Matches | Does |
|---|---|---|
| `git_diff` | a `diff --git` header or a unified diff | keeps headers and changed lines with one line of context, collapses unchanged runs, drops `index` lines |
| `grep` | >= 60 % `path:line:` lines that are not log timestamps | drops consecutive duplicates, caps 12 matches per file |
| `ls` | >= 70 % long-listing lines | keeps name, size and mode |
| `logs` | >= 60 % lines with a level or timestamp | collapses repeats, keeps every error/warning line |
| `truncate` | everything else | keeps head and tail |

The characters removed are logged per request and summed in the panel and in `/v1/usage*`.

## Ranking (`internal/rank`)

Pure and deterministic: no I/O, no model calls, no randomness. Inputs are the catalog snapshot, the
client's key state, the preset, the leaderboard rows and the time; the output is at most
`routing.top_models` candidates with their numbers and a reason, plus every rejected model with its
reason.

### Pool and rejections

The pool is `preset.models` — the ids the owner ticked in the panel — plus every free model and
every model whose promo is running of that category (ADR 0022). The added models are the ones the
client's own keys may call and that are usable; they leave the pool on their own when the promo
ends or kenari drops the model. Ranking never removes a model for being new, cheap, expensive or
unknown, and never removes a free or discounted one for want of quality evidence.

A model is rejected only when it cannot serve: not in the catalog, of another endpoint,
subscriber-only without a plan, sunsetting inside `routing.avoid_sunset_days`, or no key of this
client may call it. A model that is merely outside the top list is a *reserve*, not a rejection.

### Billing class

Each model row carries exactly one class, in this order:

| Class | When |
|---|---|
| `free` | kenari lists it free, or the id ends in `:free` |
| `plan` | the account's plan covers it (`scope_models`) and the window still has budget |
| `balance` | paid from the account balance |

### Reference cost

Every price becomes one number, the price of one reference request, so input, output and cache read
are never compared in isolation:

| Category | Reference request |
|---|---|
| chat | 4 000 input tokens (70 % of them cache reads), 600 output tokens |
| systemone | same as chat |
| images | 1 image |
| videos | 5 seconds at 720p |
| audio_speech | 1 000 characters |
| audio_transcription | 60 seconds |
| music | 1 song |

`in·(1−0.7)·P_in + in·0.7·P_cr + out·P_out`, divided by 1e6. Promo and scheduled price changes are
applied first, so a running promo lowers the number. Cache write is displayed but not ranked on: only
a minority of catalog models publish it. Free models are 0. A model with no usable price is
"unknown" and its cost score is 0.4.

Worked example, Claude Opus 5 (`In` 100 000, `Out` 500 000, `CR` 40 000 per 1M): 1 200 uncached input
= Rp120, 2 800 cached = Rp112, 600 output = Rp300 -> **Rp532**.

Every model is priced at its promo-effective rate: the listed price with promo expiry and any
scheduled change applied. That is the number used whether the model is paid from balance or covered
by a plan, because it is what kenari deducts; plan coverage shows up in the billing class and in the
two cost pools, not in the number.

Cost score within the compared pool: `cheapest = 1`, otherwise `1 / (1 + ln(cost / cheapest))` —
equal 1.000, 13 % dearer 0.891, twice 0.591, ten times 0.303.

### Quality score

One number per purpose and category, 0..1, from public leaderboards (`internal/bench`). A client
purpose picks the sources for its text preset; the pool is reordered, never changed.

| Purpose | Sources (fixed weights) |
|---|---|
| general | Arena `text` |
| coding | SWE-bench Verified 0.5, Arena `webdev` 0.3, Aider polyglot 0.2 |
| agentic | Arena `agent` |
| vision | Arena `vision` 0.5, Arena `document` 0.5 |
| bulk | none — cost only |

A source the model is missing is dropped and the remaining weights renormalise, so a model with two
of three coding sources is still comparable; the panel shows which sources were used.

**Quality is filled for text presets only.** `assignBench` reads the arenas of the client's purpose,
and every purpose maps onto chat arenas. Image, video, TTS, STT, music and System One presets
therefore carry no quality number: every model in them is `unranked`, Cheapest and Balanced order them
by cost (Balanced with quality at 0), and **Best Quality on such a preset lists nothing**. The
`text_to_image`, `text_to_video` and `asr` rows are downloaded and stored, but the ranking does not
consult them yet. See "Known gaps" in `docs/plan.md`.

**Fallback, never exclusion.** A text model with no purpose-specific score is scored on the general
arena and flagged `general score`. A model with no evidence anywhere is `unranked`: it is excluded
from Best Quality (there is nothing to rank it on) and competes on Cheapest and Balanced. A `:free`
twin uses its paid sibling's score.

The owner types no number: every score is fetched and matched onto catalog ids by name.

### The three optimizations

| Optimization | Ordering | The list |
|---|---|---|
| Cheapest | reference cost ascending, nothing else | `top_models`/2 free + `top_models`/2 plan-covered (5 + 5 at the default), each half by its own cost; a short half is topped up from the rest by cost |
| Balanced | `0.5 · cost score + 0.5 · quality` | one ranking, top N |
| Best Quality | quality only, with a floor at the median of the ranked pool | one ranking, top N; `unranked` models are excluded |

Two cost pools for Cheapest, because a free model costs nothing ever and a plan-covered one spends
plan budget: one ranking would let a cheap plan-covered model displace a free one. Equal scores are
ordered by model id, so a run is reproducible.

Every candidate carries its cost, quality, quality source, billing class, flags and a one-line
reason; every model left out carries a reason too. `xellter explain` prints the same data.

## Routing (`internal/route`)

Five rules, in this order.

1. **Session pin.** A sticky preset (the default, ADR 0025) pins the session to one model: the first
   successful request picks the model, and every later request of that session uses it, **whatever effort
   or size the request has** (ADR 0028). There is no expiry: a session moves only when its model is taken
   out of the preset, or is slow for the session (rule 5). A preset with `sticky: false` keeps no pin, and
   every turn is routed again. Pins are saved in the database (`kv`, `session_pins`) every minute and when
   the service stops, and loaded at start-up; a pin unused for seven days is dropped.
2. **Round-robin ring.** A fresh session takes the next model in the preset's ring, which runs over
   the whole top list followed by the reserve. The counter is per client and preset, so two presets never
   share one. Every model in the list is used in turn.
3. **Keys.** Free or plan-covered keys first, then pay-as-you-go; equal keys rotate. A client never
   uses another client's keys.
4. **Eviction and health.** A model is taken out of **that preset only** when it fails `evict_errors`
   times in a row (out for `evict_cooldown_minutes`), or when its last hour shows it is unreliable or slow
   (out for `quarantine_minutes`, doubling; ADR 0029). The ring skips it while it is out and the reserve
   (the pool members the top list left out, in the optimization's order) takes its place, so the preset
   keeps routing against the same number of models. A session pinned to it moves on its next request. The
   rankings are not recomputed: scores do not depend on who is out. A preset is never emptied (the last
   two models stay in). Eviction state is per client and preset and survives a rank rebuild and a restart
   (the models that are out and their strikes are saved with the health records). Direct model pins (a caller naming a real model) are not evicted.
5. **A session leaves a slow model.** If the pinned model's first token took longer than
   `repin_first_token_s` for two of the session's last three requests, the pin is dropped and that model
   goes to the end of that session's order.

The reasoning effort a request carries is forwarded and never chooses a model (ADR 0028). The only fact
kept about it is a refusal: a model that answered HTTP 400 to the field is served without it.

Rankings are cached; nothing on the request path recomputes them. The panel's Recalculate rebuilds
them all and reports the moment of the last run.

## Endpoints

| Method and path | Answers |
|---|---|
| `POST /v1/chat/completions`, `/v1/responses`, `/v1/messages` | chat, passed through as-is (OpenAI chat, OpenAI Responses, Anthropic messages) |
| `POST /v1/systemone` | System One, passed through as-is |
| `POST /v1/images/*`, `/v1/audio/speech`, `/v1/audio/transcriptions`, `/v1/videos`, `/v1/music` | media, passed through |
| `POST /v1/x/search` | the X search, passed through as-is to the key the client keeps for that service |
| `GET /v1/videos/{id}` | a video job, always through the key that created it |
| `GET /v1/models`, `/v1/models/{id}` | the client's presets plus the real models in its pools, with their catalogue prices |
| `GET /v1/session` | what this session is using right now |
| `GET /v1/usage?range=24h\|30d` | the client's traffic per model and billing kind, in kenari's shape; only successful requests count |
| `GET /v1/usage/keys` | the same traffic split by the kenari key name that served it |
| `GET /v1/usage/models?preset=` | the same traffic per preset and model |
| `GET /v1/account/balance`, `/v1/account/quota` | the account state xellter already keeps |
| `GET /healthz` | `{ok, models, catalog_age_s, degraded, log_dropped, log_lost}`: the last two count request rows thrown away because the log queue was full and rows the database refused, since the process started |

No protocol translation: a body is forwarded to kenari with only `model` rewritten (and
`stream_options.include_usage` added for streamed chat completions).

A path that is none of these is refused with 404: it is not forwarded, because a request for an
endpoint xellter does not have would otherwise be sent upstream as a model call, and its failure
would be counted against every model of the preset (ADR 0024).

### `GET /v1/session`

Authenticated with the client key; the session comes from the same headers as a normal request or
from `?session=`. Answers `object`, `session`, `client`, `preset`, `model` (the pinned real model,
else the last model the log saw), `vendor`, `billing`, `context_window`, `modalities`, `tool_call`,
`pinned_at`, `requests`, `errors`, `in_tok`, `out_tok`, `cached_tok`, `cost_idr`, `last_model` and
`models` (every model the session has used, oldest first). 400 when no session can be identified,
404 `no_session` when the session has no traffic yet.

### `GET /v1/usage/keys` and `GET /v1/usage/models`

Both take `range=24h|30d` (default 24h) and answer `{object, start, end, data}`. A key row is the
kenari key's name inside the client (never a secret), client, requests, errors, tokens in/out, cached
tokens, characters saved by RTK and cost at catalog prices. A model row is preset, model and the same
counters. `/v1/usage` remains per model with `billing_kind` (`free`, `plan`, `payg`),
`cost_micro_idr` (what was paid) and `catalog_micro_idr` (list price of the same traffic).

## Panel (`internal/admin`)

The panel is the site: it is served at the root of its host and its JSON API is at `/api` (ADR 0023).
The proxy keeps `/v1`, and `/healthz` is the proxy's. A deep link such as `/clients/demo` loads the
panel shell and the page works out the rest.

Routes: **Overview**, **Clients**, **Usage**, **Config file**. There is no Settings page: the few
remaining knobs live in `xellter.yaml` and the Config tab edits that file (ADR 0012, 0014).

| Screen | Shows and changes |
|---|---|
| Overview | status banner with alerts, 24 h requests / failures / cost at list price / characters saved by RTK, kenari plan windows and balance, hourly chart, per-client table, Refresh catalog |
| Clients | one row per client: purpose, key names, presets, 24 h requests and cost; New client (name, purpose, optional kenari key) and the xellter key shown once |
| Client detail | purpose and enabled; kenari keys with probe state and the models they may call (add, rename, remove); one card per preset: its name (renameable), optimization, the sticky-session toggle, pool picker grouped by vendor with a family checkbox and a price line, the free and discounted models xellter added on its own, "Serving now" (rank, model, billing, score, why), "Out for now" (the models taken out of the preset, the reason with its numbers, when each returns), "Taken out before" (the history), and a "Last 24 h" column on every model (median first token, failure share, requests), and "Left out" grouped by reason; Look up a session; New xellter key; Recalculate; Delete client |
| Usage | 24 h or 30 d; all clients -> one client -> its presets -> the models in a preset; totals, chart, and kenari's own plan windows and balance next to xellter's numbers |
| Config file | the YAML, checked and applied on save, with earlier versions restorable |

The model picker row is the requirement: name, billing class, context size, modalities, tool calling
and `In … Out … CR … CW …` in IDR; free and plan-covered rows show list prices greyed so the pool
can be compared, and a price the catalog does not publish is `—`.

Admin API (under `/api`): `overview`, `clients`, `client`, `presets`, `keys`, `keys/reset`,
`keys/probe`, `account-key`, `ranking`, `recompute`, `catalog/models`, `catalog/refresh`, `usage`,
`usage/drill`, `usage/keys`, `session`, `requests`, `bench`, `bench/refresh`, `config`, `rollback`,
`login`, `logout`, `logout-all`, `me`. Writes need the `X-Xellter` header; the login cookie is
HttpOnly, SameSite=Strict and scoped to `/`; `Host` must match `server.admin_hosts`.

The panel's source is `web/`: React, Vite, Tailwind and shadcn/ui. `npm run build` writes
`internal/admin/ui/dist`, which the Go binary embeds, so the shipped service is still one file and
the browser still loads nothing from a CDN. The design tokens are the three layers of the
ui-ux-pro-max reference: primitive, semantic, component (`web/src/styles/tokens.css`).

## State

| What | Where |
|---|---|
| config (clients, keys, presets, routing) | `xellter.yaml` (0600), history in `data/config-history/` |
| kenari key values | `data/secrets.env` (0600) |
| request log, usage, leaderboard rows, admin sessions, video job -> key | `data/xellter.db` (SQLite, WAL) |
| last good catalog | `data/catalog.json` |
| admin login | `data/auth-record.json` (0600), `login-state.json`, `recovery-used.json` |
| session pins | `data/xellter.db` (`kv`), written every minute and on shutdown |
| learned effort refusals | `data/xellter.db` (`effort_facts`) |
| the models that are out, their strikes, the health records of the last hour | `data/xellter.db` (`kv`, `model_health`), written every minute and on shutdown |
| when each model was taken out and why | `data/xellter.db` (`model_events`), kept as long as the request log |
| round-robin counters, key cooldowns, breakers, the token-estimate correction | memory only |

Account counters (rpm, free quota, plan ledger, client spend) are rebuilt from the request log at
start-up.

## Failure behaviour

- Catalog unreachable: keep serving the last snapshot, flagged degraded.
- Nothing to route to: `503 no_candidates`; every model of the preset evicted: `503 all_evicted`;
  no key can take the request: `429 no_capacity` (or 503 when the reason is not a limit), with
  `Retry-After` in both cases.
- A streamed chat answer that sends no first token in time is cancelled and the next model is tried; the
  last model that can still be tried is waited for.
- A stream that fails after the first byte cannot be retried; it is logged as an error.
- RTK catches its own panics and sends the original text.

## Quality sources (`internal/bench`)

| Arena key | Source | Shape |
|---|---|---|
| `text`, `webdev`, `agent`, `vision`, `document`, `text_to_image`, `text_to_video` | LMArena leaderboard dataset (Hugging Face datasets-server, `overall` category) | rating -> probability of beating the arena median, computed from the **lower** 95 % bound |
| `swebench` | swebench.com leaderboard page, embedded submission payload | `resolved` percentage / 100 |
| `aider` | Aider polyglot leaderboard YAML, best pass rate per model | pass rate / 100 |
| `asr` | Open ASR Leaderboard (Hugging Face datasets-server) | mean WER, inverted and scaled between best and worst |

Matching is by normalised name: run settings, dates, resolutions and packaging tokens are dropped,
aliases in parentheses are tried, a trailing `.0` release or `turbo` suffix may be dropped; of
several entries for one model the most voted wins. Rows are cached in SQLite and re-matched whenever
the catalog changes, without network. A source that fails does not lose the others.

Coverage measured 2026-10-26 (restated from ADR 0018): 48 of 77 text models have an Arena `text`
score, 41 have `webdev`, 29 `agent`, 29 `vision`, 9 `document`, 13 have a SWE-bench entry, 10 of 20
image models, 3 of 8 video models and 1 of 1 STT model have a score. TTS, music and System One have
no public benchmark at all. Of these, only the chat arenas are read by the ranking today, so image,
video, TTS, STT, music and System One presets are price-only and labelled `unranked` (see "Known
gaps" in `docs/plan.md`).

## Migration

`xellter migrate -config xellter.yaml` reads the pre-rewrite file and prints the new one;
`-write` keeps the old file as `xellter.yaml.pre-rewrite`. Clients, keys, hashes and notes are kept,
the purpose becomes `general`, every preset keeps its name, category and model list, and the
optimization becomes `balanced`. Notes name every preset whose pool is empty and every preset that
was dropped because its name is not one of the fixed seven.
