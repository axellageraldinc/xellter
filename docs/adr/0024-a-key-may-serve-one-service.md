# 0024 — A key may serve one service; an endpoint xellter does not have is refused

- Status: Accepted
- Date: 2026-10-27
- Deciders: owner (Axell)

## Context

1. A request for an endpoint xellter does not have (`POST /v1/x/search`) was answered by whichever
   preset matched: `route.modalityOfPath` returned `""` for an unknown path, and the check that refuses
   a preset on the wrong endpoint only ran when the path was known. So the search request was forwarded
   upstream as a chat call, kenari answered 503, and the failure was counted against the model. The proxy
   then tried the next model, and the next: one bad request fanned out over 23 models and evicted all of
   them from the preset (ADR 0020). Every later request of that client failed with `all_evicted` until the
   service was restarted, because the evictions live in memory.
2. The X search is a service of kenari, not a model: kenari's catalogue lists no search endpoint, so there
   is nothing to rank and nothing to route. It cannot be a category with a pool.
3. The key that serves it is pay-as-you-go. xellter asks kenari which models a key may call (the probe),
   and that probe is a request the owner pays for. A key dedicated to the search service must not be
   probed, and must not serve a model call either: every model call through it would drain a balance
   that exists for one endpoint.

## Decision

- **An endpoint xellter does not serve is refused** (404), for a preset and for a model named directly.
  The path decides what category may serve it; a path that maps to no category serves nothing. A failed
  forward can no longer take a whole pool out of a preset.
- **`/v1/systemone` is a known path** and maps to the `systemone` category.
- **A failure belongs to the model only when the model answered it.** A request the caller cancelled
  (a closed tab, a stopped stream) is not a failure of the model and not of the key: it is logged as
  "cancelled by the client" and leaves both untouched. Counting it evicted healthy models and opened
  the key's breaker after a few interruptions.
- **A key may declare a capability** (`capability: search`). Such a key:
  - is never probed,
  - never takes part in model routing (it is not in the pool of any preset),
  - serves its own endpoint only, and the request is forwarded as it came: kenari owns the shape of the
    request and of the answer,
  - is shown in the panel as serving that service, so the owner can see what the client is made of.
- **The catalogue prices are published** in `/v1/models`, so a client can show what a model costs without
  a second source: the token prices for a chat model, the unit lines for a media model.

## Consequences

- The search path is one key and one endpoint. It is not a preset, so it has no ranking, no pool and no
  eviction: a failed search is one failed search.
- A **successful** search appears in the client's usage as its own row (`x/search`, paid), because
  kenari bills it from the balance. A failed search is not charged and therefore not counted as usage.
- A pay-as-you-go key is only ever spent on the calls the client asked for.
- The proxy answers a request for an endpoint it does not have with 404 and does not touch the upstream.
- A model named directly is refused on an endpoint of another kind, like a preset is.
