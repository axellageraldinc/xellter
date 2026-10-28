# Model rotation: you pick the pool, xellter keeps it healthy

A preset is a pool of models. You do not have to watch how those models behave. xellter
measures every one of them, per client and per preset, on the traffic your app really sends,
and rotates around the ones that are slow or failing. They come back by themselves when they
recover. This is the main thing xellter does; the rest (ranking, RTK, the panel) supports it.

Nothing here has a switch you must set. The defaults work, and every number can be changed in
the Config tab (`routing`, see [configuration.md](configuration.md)).

## What you do and what xellter does

| You | xellter |
|---|---|
| Tick the models a preset may use and pick one optimization | Orders the pool, spreads traffic over the best ten, and keeps the rest as a **reserve** |
| Nothing, when a model starts to queue for 30 seconds | Moves the request to the next model, then stops sending traffic to the slow one |
| Nothing, when a model starts failing | Takes it out of that preset, brings the next best in, and tests it again later |
| Nothing, when a conversation sits on a bad model | Moves that conversation to another model |

## The loop

```
request ──► pick the next model in the ring ──► send ──► first token in time? ──┐
                ▲                                                               │ yes: stream it, record the speed
                │ no / error: retry once, then the next model                   │
                └───────────────────────────────────────────────────────────────┘
                                                                                 
every attempt is recorded per (client, preset, model):
  too many failures in a row ─┐
  median first token too slow ├─► the model is OUT of that preset for a while,
  too large a share failed   ─┘   the reserve takes its place in the ring
  when the time is up ─► it serves again on a clean record and is judged again
```

### 1. Round-robin over the best ten

A new conversation takes the next model in the preset's ring. The ring covers the top
`top_models` (10) of the optimization's order, so traffic is spread instead of piled on one
model that then becomes the next to queue. Pool models outside the top ten are the reserve:
they serve nothing while the list is healthy.

### 2. A request that goes wrong is moved at once

- A retriable error is retried once on the same model, then the next model is tried.
- A **streamed chat answer that sends no first token** within `first_token_timeout_s` (15 s,
  plus 1 s per 10 000 prompt tokens) is cancelled and the next model is tried. The caller never
  sees the stall. The last model that can still be tried is never timed out.
- If every model fails, the caller gets a clear error (`503 all_evicted`, `no_candidates`) and
  the headers say how many attempts were made.

### 3. A model that does badly goes out of the preset

xellter keeps a record of the last hour for each `(client, preset, model)`. A model goes out when:

| Why | Rule (defaults) | Out for |
|---|---|---|
| **errors** | `evict_errors` (3) failures in a row | `evict_cooldown_minutes` (12 h) |
| **slow** | median first token above `slow_first_token_s` (10 s) | `quarantine_minutes` (30 min), doubling |
| **unreliable** | `fail_percent` (40 %) of its requests failed | `quarantine_minutes` (30 min), doubling |

Slow and unreliable are only judged after `health_min_requests` (6) requests, so one bad answer
never removes a model. A 4xx, or a caller that hung up, is not the model's fault and is not counted.

The judgement is per client and per preset. Your coding agent's large prompts can take a model out
of the coding preset without touching a chat preset that sends short ones.

A preset is **never emptied**: a model is not taken out if fewer than two others would remain.

### 4. It comes back by itself

When the time is up the model serves again with a clean record, judged on half as many requests.
If it is bad again it goes out for twice as long (30, 60, 120 minutes, up to `evict_cooldown_minutes`).
When it passes, the next time out starts from 30 minutes again. A restart does not forgive it: who is
out, and for how long, is saved in the database.

### 5. A conversation leaves a bad model

By default a preset is **sticky**: a conversation stays on one model, so the provider's prompt cache
stays warm. If the pinned model is taken out, the conversation moves on its next request. If the model
is merely slow for that conversation (the first token took longer than `repin_first_token_s`, 15 s,
in 2 of its last 3 requests), the conversation also moves, and that model goes to the end of its order.
Turn `sticky` off on a preset to route every turn again.

### 6. Ties go to the faster model

When two models cost or score the same, the one with the shorter median first token over the last seven
days goes first, raised by its failure rate (ADR 0032). A model nobody has measured counts as average.

## What you see

Open a client in the panel. Each preset card shows:

- **Serving now**: the models actually in the ring, with rank, billing, score and why.
- **Out for now**: each model that is out, the reason with its numbers (for example
  `slow: median first token 12.3s over its last 7 requests (limit 10s)`) and when it returns.
- **Taken out before**: the history of every time a model went out.
- A **Last 24 h** column on every model: median first token, failure share and request count.

The same line is written to the log (`journalctl --user -u xellter`). Every response carries
`X-Xellter-Model` (who answered) and `X-Xellter-Attempt`.

## What it does not do

- It does not add models to your pool. A paid model is routed only when you ticked it; free and
  discounted models join on their own and can be unticked per preset.
- It does not rank by speed. Speed only breaks ties and decides who is out, so traffic is still
  spread over the ring.
- It does not change a model's score. Scores come from public leaderboards and do not depend on who
  is out.

## Tuning

You usually should not. If a model you rely on keeps being taken out for slowness on large prompts,
raise `slow_first_token_s` or `first_token_timeout_s`; if you want quicker action, lower `evict_errors`
or `fail_percent`. Every key is documented in the Config tab and in [configuration.md](configuration.md).

Design records: [ADR 0020](adr/0020-round-robin-top-ten-with-per-preset-eviction.md) (ring and
eviction), [0025](adr/0025-sticky-is-a-per-preset-toggle.md) (sticky),
[0029](adr/0029-slow-and-unreliable-models-go-out-of-the-preset.md) (slow and unreliable models),
[0032](adr/0032-ties-are-broken-by-measured-speed.md) (ties).
