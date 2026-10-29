# Plan and phases

Status: **the ranking/routing rewrite is done** — `./scripts/ci.sh` is green and the running service
uses the new engine. `docs/rewrite-plan.md` is the design it was built from (a historical document: where
it differs from the ADRs, the ADRs and `docs/architecture.md` are right). ADRs 0017–0030 record the
decisions. Rollout and tuning continue (see "Next").

## Rewrite phases

| Phase | Deliverable | Status |
|---|---|---|
| 0 | `docs/rewrite-plan.md`, committed | done |
| 1 | New config schema: seven `routing` knobs, purpose, explicit pools, one preset per category, retired-key refusal, `xellter migrate` | done |
| 2 | Ranking: reference cost per category, three optimizations, the two cost pools for Cheapest, purpose scoring with fallback, `unranked`, reasons for every candidate and rejection | done |
| 3 | Routing: round-robin ring over the top ten, session pin, retry once, per-preset 3-strike eviction with cooldown, 5-minute refresh, Recalculate | done |
| 4 | Panel: client detail with purpose, keys and one card per preset; vendor-grouped model picker with family checkbox and the price line; Serving now; eviction list; Left out by reason; usage drill-down; `GET /v1/session`, `/v1/usage/keys`, `/v1/models` | done |
| 5 | Benchmark sources: Arena multi-leaderboard, SWE-bench Verified, Aider polyglot, Open ASR; name matching with aliases; `unranked` labelling | done for fetching, matching and the chat arenas; the media arenas are stored but not yet read by the ranking (see Known gaps) |

## Owner-requested changes (2026-10-27)

| Change | Decision | Status |
|---|---|---|
| The panel was rebuilt | React, Vite, Tailwind and shadcn/ui on the ui-ux-pro-max token architecture; a clean neutral console look, light and dark, mobile-first | done (ADR 0023) |
| The panel moved to the site root | the panel is served at `/`, its API at `/api`, the proxy keeps `/v1`; the session cookie covers `/` | done (ADR 0023) |
| Presets are named by the owner | the name is the model the app asks for; the category says what it serves; one preset per category still holds | done (ADR 0021) |
| Free and discounted models are always in the pool | every free model and every running promo of the category is added to every preset of that category, and leaves it again when the promo ends | done (ADR 0022) |
| Stickiness is a per-preset toggle | a preset keeps one model per session (the default) or routes every turn again; the global `routing.sticky_session` is refused on load | done (ADR 0025) |
| The usage history was lost | a stray `rm` deleted the live SQLite database, and the service came back as if it had never run: the new database started at request id 1 and the usage page showed nothing. Nothing in the code deleted it | fixed: a daily backup timer snapshots the database, the secrets and the config outside `data/`, and a marker file makes a lost database visible in the log instead of silent |
| A reasoning effort had to reach the model | the owner set the effort to "none" and the answer came back after nine minutes with 46,773 output tokens: the effort was dropped in the browser and the gateway forwarded it blindly, where 3 of 17 measured models ignore it and 2 reject it with HTTP 400 | done: the gateway measures what each model does with an effort, routes "none" to a model that honours it, serves a refusing model without the field, and advertises the pool's largest context window so the client stops compressing early (ADR 0026) |
| What the gateway learned had to survive a restart | the facts were in memory, so every restart made the gateway blind again: the first request was served by a model that ignores the effort, and the client waited for an answer it did not ask for | done: the facts are written to the database on a timer and on shutdown, and read back on the next start; a fact older than 30 days is dropped and the model is measured again, a refusal is kept however old it is (ADR 0027) |
| The effort routing hurt more than it helped | measured on real traffic, most models ignore or refuse "none", and routing by it moved a session's side calls (titles, summaries) off the pinned model and moved the pin with them, so a conversation changed model several times in minutes | done: the effort is forwarded and chooses nothing; a refusing model is still served without the field; the pin is kept across restarts and the pinned model stays first (ADR 0028) |
| Replies were slow | free upstreams queue for 10–90 s before the first token; a conversation that landed on one stayed slow because of stickiness; a stream whose headers arrived but whose first token did not could not fail over | done: models are judged per client and preset on first-token wait and failure rate and taken out for a while (30 min, doubling), a silent stream moves to the next model, a session leaves a slow model, the panel says who is out and why (ADR 0029) |
| Clients without a session id shared one pin | a long system prompt made every conversation of an agent hash to the same session | done: told apart by the first user message (ADR 0030) |
| Health survived nothing | a restart forgave every slow model, the dead effort-learning code still ran, rows could be lost from the log without a trace, the size of a request was guessed as bytes/4 and the panel showed no speed | done: the models that are out are saved and their history is kept (ADR 0029), the effort code keeps only refusals (ADR 0028), lost log rows are counted on `/healthz`, the size is estimated from the JSON and corrected by what the upstream reports (ADR 0031), and the preset card shows the first-token wait and failure share of every model |
| The free slots went to the names that sort first | all free models cost Rp 0, so ties were broken by name; and the reserve was part of the ring, so a preset with 23 models spread fresh sessions over all 23 although it said it routes against 10 | done: ties are broken by measured first-token speed (ADR 0032), and the ring is the top list only, the reserve replacing a model that is out one for one (ADR 0020 addendum) |
| The model name showed from the second turn | `/v1/session` answered 404 until the request was logged, which happens when the answer ends | done: it answers from the pin |

## Earlier phases (first build)

| Phase | Deliverable | Status |
|---|---|---|
| P0 Foundation | repo, docs, ADRs, CI (GitHub-hosted, pinned actions), Go skeleton | done |
| P1 Proxy core | OpenAI + Anthropic wire, streaming pass-through, per-client keys, usage accounting, SQLite log, `/healthz` | done |
| P2 Routing | cooldown + breaker, catalog sync | done, superseded by phase 3 |
| P3 Auto-rank | free/plan/promo rules, tier quotas, plan ledger, weighted score, quality gate | done, superseded by phase 2 |
| P4 Dashboard + login | panel, scrypt + TOTP login, config history + rollback | done |
| P5 RTK | diff / grep / ls / log / truncate filters | done; now always on with fixed thresholds |
| P7 Client-centric model | clients own kenari keys; account key for real plan numbers; panel on Tabler | done; the panel was rebuilt in React (ADR 0023) |
| P6 Hardening + rollout | systemd unit, Tailscale exposure, REGISTRY, secret scan, clients moved one at a time | in progress |

## Deliberately not done

| Not done | Why |
|---|---|
| Per-client budget (`daily_budget_idr`) | not requested. Spend is displayed per client, never capped. The code keeps a spend counter, but nothing enforces a limit |
| `passthrough` globs | two ways to grant access is one too many: being in a preset pool is the allow list (ADR 0019) |
| An RTK knob or switch | RTK is always on, all filters, fixed thresholds (ADR 0017). No config key, no request header |
| Human-typed quality scores (`benchmarks:` map) | the owner does not maintain numbers that go stale. Every score is fetched and matched onto catalog ids (ADR 0018) |
| A weights/quality-gate/strategy vocabulary | one optimization per preset replaces weights, `min_quality`, `rr_band`, `max_candidates`, `strategy` and `mode` (ADR 0017) |
| Generated or seeded presets | the owner creates one preset per category and names it; nothing appears by itself (ADR 0021) |
| A round-robin band | equal scores are adjacent in the ring, so ties rotate without a knob (ADR 0020) |
| A model or judge producing a score | scoring is a deterministic function of the catalog, the account state and the leaderboard snapshot (ADR 0016) |
| A switch for the free and discounted models | they are always in the pool (ADR 0022); a knob would only decide whether the owner's rule is followed |

Client-level `passthrough`, `daily_budget_idr` and `seeded` are not even refused: they are silently
ignored, so an old file still loads. Retired sections and preset keys are refused by name, pointing at
ADR 0017.

## Known gaps (code vs. rewrite plan)

Found while rewriting the docs; not fixed here because the fix is code, not prose.

| Gap | Detail |
|---|---|
| Quality is text-only | `rank.assignBench` switches on the client's purpose and every purpose maps to chat arenas, so image, video, TTS, STT, music and System One presets get `source: "none"` for every model. Their Balanced order is cost-only (quality 0) and **Best Quality lists nothing**. The `text_to_image`, `text_to_video` and `asr` rows are fetched and stored but never read by the ranking. The plan says each media category has exactly one metric |
| The Cheapest halves follow `top_models` | The plan says five free + five plan-covered; the code uses `top/2` per half, which is 5 with the default `top_models: 10`. Lowering the knob lowers the halves too, which is consistent but not what the plan states |
| No retry-once switch | The plan lists `routing.retry_once: true`; the code has no such key — retry-once is unconditional in the proxy. The docs describe the behaviour, not a knob |
| Sticky is a boolean, not minutes | The plan lists `sticky_minutes: 0`; the code has a `sticky` flag on the preset (ADR 0025), i.e. a pin that never expires. Behaviour matches the intent |
| `/v1/usage/keys` keys on the upstream key | It is scoped to the calling client and splits that client's traffic by the upstream key's name, which is what the endpoint is for. A client's total is `/v1/usage`. Not a gap; the earlier wording ("by xellter API key") was the plan's, not the code's |

## Rollout
1. Run on the tailnet only; owner tests with curl and the panel.
2. Move one app at a time, the smallest key first: change `base_url` and key in its profile, keep
   the old direct key as a rollback.
3. Watch Overview and Usage for a few days; adjust a preset's optimization or its pool.
4. Open the public domain (`xellter.<your-domain>`) and flip the repo to public when the owner says so.

## Next (not scheduled)
- Panel: change password / re-enrol 2FA (today: CLI), request playground.
- Prometheus `/metrics` (considered and skipped for now: the request log and `/healthz` carry what is needed).
- A provider *type* other than `kenari` (ADR 0040 made the seams real: providers, per-provider keys and accounts, a merged
  catalog; only the `kenari` wire protocol exists).
- Weekly digest to the operator (discounts ending, new models, sunsets).
- `benchmarks` coverage: more sources for TTS, music and System One, which are price-only today.

## Performance targets
Binary < 20 MB, idle RSS < 30 MB, added latency p99 < 3 ms, streaming without buffering. Nothing on
the request path recomputes a ranking or fetches the catalog.
