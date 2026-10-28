# xellter rewrite — plan

Status: **historical.** The rewrite shipped; later ADRs (0020 to 0030) changed parts of it: the eviction rules
and the reserve (0020, 0029), per-preset stickiness (0025), the effort (0026, 0028), pins and session
identity (0028, 0030). Read `docs/architecture.md` for how xellter works now.
Original status: accepted, in progress. Owner: maintainer. Date: 2026-10-26.
Supersedes the ranking and routing parts of ADR 0003, 0010, 0013, 0016 and the panel rules of ADR 0012, 0014.

## Why

The current xellter ranks models with five weighted components, per-preset weights, a quality gate, three
bonuses scaled by a cost weight share, a round-robin band and a trial pool. It works, but it is more
machinery than the job needs: the owner asked for a router, not a scoring laboratory.

This rewrite keeps everything that makes xellter a working gateway — kenari catalog sync, key probing,
plan and balance reading, rate limits, streaming, request logging, RTK, the panel shell — and replaces
the ranking and routing brain with three fixed optimizations and one round-robin.

## The 20 requirements

The owner's list, verbatim in intent, is the contract. Every phase below is traced back to it.

| # | Requirement |
|---|---|
| 1 | Register clients |
| 2 | A client has multiple kenari keys; xellter fetches every model those keys can call; duplicates merged, cheapest wins |
| 3 | A client has multiple presets, each tied to a category; exactly one preset per category |
| 4 | Each preset has one optimization: Cheapest, Balanced, Best Quality |
| 5 | Cheapest is cost alone — input, output, cache, promo included |
| 6 | Balanced is cost 50 : benchmark 50 |
| 7 | Best Quality is the benchmark of that category |
| 8 | Each preset chooses its own models from a dropdown grouped by family; clicking a family selects all of it |
| 9 | Dropdown row: name, free/plan/balance, context size, modality, tool calling, `In … Out … CR … CW …` in IDR |
| 10 | Equally scoring models rotate automatically |
| 11 | RTK always on, global, not configurable |
| 12 | One model per session, no switching mid-session |
| 13 | Routing is round-robin over every model in the preset |
| 14 | Top 10 models are routed against |
| 15 | Retriable error: retry once, then next model |
| 16 | 3 errors in 1 hour removes the model from that preset; replaced by the next best |
| 17 | Ranks recalculated every 5 minutes, plus a Recalculate button per client |
| 18 | Usage for all clients and per client; per client drill down to preset, then model |
| 19 | kenari's own usage and balance visible |
| 20 | Benchmark metric per category |

Decisions taken during the interview, which are part of the contract:

- **No human preference rating typed by the owner.** Quality comes from public benchmark sources only.
- **A client has a purpose** (General, Coding, Agentic, Vision & documents, Bulk) that changes how its text
  preset is scored. Purpose reorders the pool; it never changes pool membership.
- **Purpose covers text only.** Image, video, TTS, STT and System One each have exactly one metric.
- **Fallback, not exclusion.** A model with no purpose-specific score is scored on the general text arena
  and marked `general score` in the panel. It is never dropped for lack of a number.
- **Cheapest uses two pools of five** (free, plan-covered), so free and plan-covered models never compete.
- **Cost is one number**: the reference request. Cache write is displayed but not ranked on.
- **Eviction beats stickiness**: a session moves when its model is evicted.
- **3 consecutive-ish errors within 1 hour**, then a 12-hour cooldown for that model in that preset.
- **Protocols**: OpenAI chat, OpenAI responses and Anthropic messages are accepted as-is, pass-through,
  no translation. The key may arrive as `Authorization: Bearer …` or `x-api-key`.
- **New endpoints**: session introspection and per-key usage (see "Endpoints").
- **Family = vendor** (kenari's `owned_by`), because it is authoritative and it matches how the owner
  thinks about models. Name prefixes would split `hy3` from `hunyuan-*`, both Tencent.

## What is deleted

| Gone | Was |
|---|---|
| `Weights` (cost/benchmark/reliability/context) | replaced by the three optimizations |
| `free_bonus`, `plan_bonus`, `promo_bonus_max`, `plan_cost_factor`, `promo_min_remaining_hours` | folded into the cost ordering |
| `min_quality`, `rr_band`, `max_candidates`, `strategy` | one quality rule for Best Quality; top 10 fixed; always round-robin |
| `require` gates (tool_call, vision, reasoning) | model choice is explicit in the dropdown |
| reliability and context as ranking components | eviction handles failure; context is displayed only |
| `mode: combo` | a preset is a pool plus an optimization; a one-model pool is the combo |
| per-client `daily_budget_idr`, `rtk`, `passthrough` | not requested |
| generated presets (`seedPresets`) | presets are created by the owner, one per category |
| `benchmarks:` config map | benchmark sources are fetched, not typed |
| RTK knobs | RTK is always on, all filters, fixed thresholds |

Config surface drops from 88 knobs to about 30. `xellter.yaml` is rewritten by the migration, not by hand.

## Data model

```yaml
server:      # unchanged: listen, data_dir, secrets_file, admin_hosts, body, retention, trusted_proxy, session_hours
kenari:      # unchanged: base_url, sync_minutes, user_agent, timeouts, account_key_env

routing:     # the few knobs that survive
  sticky_minutes: 0          # 0 = a session keeps its model for as long as the session lives
  rank_refresh_minutes: 5
  top_models: 10
  retry_once: true
  evict_errors: 3            # errors within the window
  evict_window_minutes: 60
  evict_cooldown_minutes: 720
  avoid_sunset_days: 3

clients:
  - name: demo
    enabled: true
    purpose: general          # general | coding | agentic | vision | bulk
    keys:                     # kenari keys, secret in secrets.env
      - name: main
        key_env: KENARI_KEY_DEMO
    presets:                  # at most one per category
      text:
        category: chat
        optimization: balanced   # cheapest | balanced | best_quality
        models: [claude-opus-5, gpt-6-sol, ...]   # the pool, chosen in the panel
```

Preset categories and their names are fixed, so a client cannot hold two of one kind:

| Category | Preset name | kenari endpoint |
|---|---|---|
| chat | `text` | `/v1/chat/completions`, `/v1/responses`, `/v1/messages` |
| images | `image` | `/v1/images/*` |
| videos | `video` | `/v1/videos` |
| audio_speech | `tts` | `/v1/audio/speech` |
| audio_transcription | `stt` | `/v1/audio/transcriptions` |
| music | `music` | `/v1/music` |
| systemone | `systemone` | `/v1/models?modality=systemone` |

System One is the sixth category. kenari serves 11 System One models on its own catalog endpoint; they
are fetched, deduped and selectable like any other model, and priced the same way. They are not text
models and never enter a text pool.

### Model universe and dedup

For each client, the universe is every model at least one of its keys may call. Dedup is by model id:
one row per model, no matter how many keys can reach it. The key is then chosen per request by billing
class, not by the model: free first, then plan-covered, then pay-as-you-go. Since kenari prices per model,
"cheapest wins" is decided once per model, not once per key.

### Billing class

Each model row shows exactly one class, in this order: `free`, `plan`, `balance`.

- `free` — the model is free at kenari, or its id ends in `:free`.
- `plan` — the account's plan covers it (`scope_models` match) and the plan window still has budget.
- `balance` — paid from the account balance.

## Ranking

Pure, deterministic, no I/O, in `internal/rank`. Inputs: catalog snapshot, the client's key state, the
preset definition, the benchmark snapshot, and the eviction state. Output: an ordered list of at most 10
candidates, each with its numbers and a reason, plus the rejected models and why.

### Reference cost

Every price becomes one number, the price of one reference request, so input, output and cache read are
never compared in isolation:

| Category | Reference request |
|---|---|
| chat | 4 000 input tokens, 70 % of them cache reads, 600 output tokens |
| images | 1 image |
| videos | 5 seconds at 720p |
| tts | 1 000 characters |
| stt | 60 seconds |
| music | 1 song |
| systemone | 4 000 input tokens, 600 output tokens |

Formula for chat: `in·(1−c)·P_in + in·c·P_cr + out·P_out`, divided by 1e6. Promo and scheduled price
changes are applied first, so a running promo lowers the number. Cache write is shown in the panel but not
part of the number: only 21 of 110 catalog models publish it, so including it would favour those 21.

Worked example, real prices for Claude Opus 5 (`In` 100 000, `Out` 500 000, `CR` 40 000 per 1M):
1 200 uncached input = Rp120, 2 800 cached = Rp112, 600 output = Rp300 → **Rp532**.

For plan-covered models the same formula uses the **promo-effective** price, because that is what kenari
deducts from the weekly and monthly plan balance. No promo means list price.

### Cost score

`cheapest = 1`, else `1 / (1 + ln(cost / cheapest))` within the pool being compared. Equal price 1.00,
13 % dearer 0.89, twice 0.59, ten times 0.30. A smooth curve, so small differences still count and an
expensive model does not collapse to zero. Unknown price scores 0.4.

### Benchmark score

One number per category, 0..1, from the sources below. Higher is better. STT inverts WER.

| Category | Source | Metric | Coverage measured 2026-10-26 |
|---|---|---|---|
| chat — General | Arena `text` | win probability vs the arena median, lower 95 % bound | 48 / 77 |
| chat — Coding | SWE-bench Verified + Arena `webdev` + Aider polyglot | see composite below | 41 / 77 have webdev; SWE-bench 13 entries; Aider reachable |
| chat — Agentic | Arena `agent` | published score, lower CI bound | 29 / 77 |
| chat — Vision & documents | Arena `vision` + Arena `document` | mean of the two win probabilities | 29 and 9 |
| chat — Bulk | none | cost only; Best Quality is not offered | 77 / 77 |
| images | Arena `text_to_image` | win probability | 10 / 20 |
| videos | Arena `text_to_video` | win probability | 3 / 8 |
| audio_transcription | Open ASR Leaderboard | mean WER over 8 sets, inverted and percentile-scaled | 1 / 1 |
| audio_speech | none | price only, marked unranked | 0 / 2 |
| music | none | price only, marked unranked | 0 / 2 |
| systemone | none | price only, marked unranked | 0 / 11 |

**Coding composite** (fixed weights, not configurable): SWE-bench Verified 50 %, Arena webdev 30 %,
Aider polyglot 20 %. A model missing a source is scored on the remaining weights, renormalised, and the
panel shows which sources were used. This is why the composite is stated as weights rather than a sum.

**Fallback rule.** A text model with no score in the client's purpose falls back to the General arena
score and is flagged `general score`. It still competes, and it can still reach Best Quality. The panel
says so. A model with no score anywhere is `unranked`: it is excluded from Best Quality (there is nothing
to rank it on) and competes on Cheapest and Balanced.

**Price proxy is gone.** The old price-percentile estimate pretended to be quality. It is removed;
`unranked` is the honest replacement.

**Sources are fetched, never typed.** The owner does not enter a single number. The Arena dataset, the
Open ASR Leaderboard and the Aider leaderboard are downloaded on a schedule, cached in SQLite, and matched
onto catalog ids by name with the existing normaliser (drop run settings, try aliases). SWE-bench is
scraped from the leaderboard page's embedded payload, which was verified to contain a clean `resolved`
percentage per submission.

### The three optimizations

| Optimization | Ordering | Pool of 10 |
|---|---|---|
| Cheapest | reference cost ascending, and nothing else | **5 free + 5 plan-covered**, each half ordered by its own cost. A half that cannot fill 5 is topped up from the other half, then from `balance` models by cost |
| Balanced | `0.5 · cost_score + 0.5 · benchmark` | single ranking, top 10 |
| Best Quality | `benchmark` only, floor at the pool median | single ranking, top 10; `unranked` models excluded |

Rationale for the two cost pools: a free model and a plan-covered model are not comparable on price — the
first costs nothing ever, the second costs plan budget. Putting them in one ranking would let a cheap
plan-covered model displace a free one, which is the wrong answer for "cheapest".

The top 10 is always ordered best-first. Every model in it is used; the order decides who goes first and
who replaces an evicted model.

### Ties

Models with an equal score are ordered by model id for determinism, and the router rotates over equal
models automatically (requirement 10). The owner never configures this.

## Routing

`internal/route`, unchanged in shape: build the attempt order, let the proxy try it.

1. **Session model.** A session is identified by `X-Xellter-Session`, `X-Session-Id`,
   `X-Claude-Code-Session-Id`, `prompt_cache_key`, `user`, or a hash of the first messages. The first
   successful request pins the session to one model; every later request of that session uses it, for the
   life of the session (`sticky_minutes: 0`). Requirement 12, no mid-session switching.
2. **Fresh session.** Pick the next model in the round-robin ring over the preset's top 10. The ring is
   per preset and per client, so two clients never share a counter. Equal-scoring models are consecutive
   in the ring, which satisfies requirement 10 without a band.
3. **Key choice** for the chosen model: free, then plan-covered, then balance; equal keys rotate.
4. **Attempts.** A retriable error (5xx, timeout, 429 with `Retry-After`, network) retries the same model
   **once**. A second failure, or a non-retriable error, moves to the next model in the ring. Requirement 15.
5. **Eviction.** Three errors for a model inside 60 minutes evicts it **from that preset only**, for
   12 hours. It is replaced by the next model below the top 10. An evicted model is skipped while its
   cooldown lasts; a session pinned to it moves to the replacement, so eviction beats stickiness.
6. **Refresh.** Rankings are recomputed every 5 minutes, after every catalog sync, and on demand from the
   panel's Recalculate button, which recomputes every preset of one client.

Eviction state is per preset, in memory, and survives a rank recalculation (it is keyed by client, preset
and model). It is deliberately not persisted: a restart forgives a model, which is the safe direction.

## Endpoints

Existing, kept: `GET /v1/models`, `GET /v1/models/{id}`, `GET /v1/usage?range=24h|30d`,
`GET /v1/account/balance`, `GET /v1/account/quota`, `GET /v1/videos/{id}`, `GET /healthz`.
Protocols accepted: `/v1/chat/completions`, `/v1/responses`, `/v1/messages`, images, audio, videos, music.

New, requirement 12/18:

**`GET /v1/session`** — what this session is actually using right now. Authenticated by the client key
plus the session header (or a `session=` query parameter). Answers: session id, preset, the real model
pinned to it, its vendor, its billing class, context window, tokens used so far in the session, request
count, the timestamp of the pin, and the remaining context (`context_window − tokens_in` of the last
request). Answers 404 with a reason when the session has no pin yet, so a client can distinguish "not
started" from "unknown".

**`GET /v1/usage/keys`** — usage broken down by xellter API key, which is what the owner needs to see per
client. Today `/v1/usage` aggregates per model for the calling client; the new shape adds the key
dimension and, for the admin, every client. Fields: key hint, client, requests, errors, tokens in/out,
cached tokens, RTK characters saved, cost at catalog prices, and the window.

**`GET /v1/usage/models`** — per model for a client and window, with billing class and the preset that
served it. This is what the panel's drill-down reads.

Requirement 19 needs no new work: kenari's quota and balance endpoints already answer and are already
shown. Verified live: plan "Kreator", month Rp 154 125 of Rp 600 000, week Rp 3 766 of Rp 150 000,
balance Rp 6 167.45.

## Panel

Routes: Overview, Clients, Usage, Config. Settings disappears as a page — the few remaining knobs live in
`xellter.yaml` and the Config tab edits that file, which is what ADR 0012 already decided.

**Clients list** — one row per client: name, purpose, enabled, key count, presets, requests and cost in
24 h. A "New client" button asks for a name, a purpose and an optional kenari key.

**Client detail** — the whole configuration of one client on one page:

- Purpose dropdown (General, Coding, Agentic, Vision & documents, Bulk).
- Keys: name, hint, probe state, the model count that key allows.
- One card per preset, each with a category header and an optimization selector (Cheapest, Balanced,
  Best Quality).
- **Model dropdown**, grouped by vendor family, each family a checkbox that selects all of its models in
  that category. A row reads exactly as requested:

  ```
  ☐ claude-opus-5        plan   1.0M ctx   text+image   tools   In Rp100k Out Rp500k CR Rp40k CW Rp100k
  ☐ claude-haiku-5-5     free   200k ctx   text         tools   In Rp0 Out Rp0 CR Rp0 CW —
  ```

  Name, billing class, context size, modalities, tool calling, and the price line in IDR with `Rp`. Cache
  write shows `—` where kenari publishes no value. Free and plan-covered rows still show list prices,
  greyed, so the pool can be compared.
- **Recalculate** button: recomputes every preset of this client and shows when the last ranking ran.
- **Serving now**: the preset's top 10 in order, with score, billing class, why it ranks where it does,
  and the models left out with the reason.

**Usage** — all clients at once, then a client, then its presets, then the models inside a preset. Windows
24 h and 30 d. Columns: requests, errors, tokens in/out, cached, RTK saved, cost at catalog prices. A
separate strip shows kenari's own plan windows and balance, so "what xellter thinks it used" and "what
kenari says" sit next to each other.

## Migration

The live server has three clients and four keys, one of them the read-only account
key. The migration must not lose them.

1. Load the current `xellter.yaml` with the current schema.
2. Map each client to the new shape: name, enabled, key list, and a purpose of `general` (a guess the owner
   can change in one click).
3. For each client, keep the presets it has today as pools: take the current top candidates per category
   as the new explicit `models` list, so behaviour does not jump on day one.
4. Write the result to a new file, keep the old one as `xellter.yaml.pre-rewrite` outside the repo, and
   refuse to start if the new file fails validation.
5. Secrets stay in `secrets.env`, untouched. Client API keys stay as their hashes.
6. Verify by comparing the old and new ranking for one client before and after.

The old routing stays reachable behind a build tag until the new one passes its tests, so the running
service is never left without a working router.

## Phases and verification

| Phase | Work | Verified by |
|---|---|---|
| 0 | This document, committed | the commit |
| 1 | New config schema, categories, one-per-category rule, purpose, pool, family grouping, migration | `go test ./internal/config`, migration dry-run on the live config, diff of before/after |
| 2 | Ranking: reference cost, three optimizations, two cost pools, purpose scoring, unranked | table-driven tests with real catalog fixtures; worked examples from this document asserted |
| 3 | Routing: round-robin ring, session pin, retry once, 3-strike eviction, 5-minute refresh, Recalculate | `go test ./internal/route`; a fake kenari that fails on demand |
| 4 | Panel: client detail, model dropdown, serving-now, usage drill-down, new endpoints | run the binary against a fake kenari and click through; `curl` the new endpoints |
| 5 | Benchmark sources: Arena multi-leaderboard, SWE-bench, Aider, ASR, unranked labelling | fetch each source for real, assert coverage numbers from this document, no network in tests |

Every phase ends with `./scripts/ci.sh` green (gofmt, vet, tests, JS syntax). Tests never touch the
network or a paid API; each source is faked with `httptest`.

Smoke run before the rewrite is called done: start the binary against a fake kenari with a preset of three
models, send eleven requests, and assert the ring used all three, that a session stayed on one model, that
a failing model was retried once and then skipped, and that three failures evicted it from that preset
only.

## Risks

| Risk | Mitigation |
|---|---|
| Benchmark name matching is patchy (3 of 8 video, 13 SWE-bench entries) | `unranked` / `general score` labels; coverage numbers are printed in the panel |
| Arena is human preference | it is other people's preference, not typed by the owner; the only proven broad source |
| A cheap model with no quality evidence wins Cheapest | accepted by the owner: Cheapest is cost only |
| Eviction in memory is lost on restart | deliberate; a restart forgiving a model is the safe direction |
| The migration changes behaviour on day one | pools are seeded from the current top candidates, and the old ranking is compared before cutover |
| Panel work is the bulk of the effort | phases 1–3 are useful without it; the panel is phase 4 and can be reviewed on its own |

## ADRs to write with this work

- 0017 — Three optimizations replace weighted scoring.
- 0018 — A client has a purpose that changes text scoring.
- 0019 — Model pools are explicit; families are vendors.
- 0020 — Round-robin over the top 10 with per-preset eviction.
