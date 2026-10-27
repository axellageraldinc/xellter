# 0015 — The proxy answers kenari's usage and account endpoints

- Status: Accepted
- Date: 2026-10-25
- Deciders: maintainer

## Context
A client reads its usage from the provider it is configured with. The first client to sit
behind xellter knows two shapes: the session log of the agent it drives (`/api/sessions`, per
session, the *preset* name as the model) and the provider's own
usage endpoint (`/v1/usage`, per *real* model, with tokens, price and billing kind).

When a client is pointed at kenari directly, it reads kenari's `/v1/usage` and shows every model
that was really used. When the same client is pointed at xellter, that endpoint does not exist, so
the per-model view is gone: the client falls back to the session log, where the model is
the preset ("text"), and the real model behind it is invisible.

The owner wants the per-model view to work through xellter, and wants the client to keep
receiving only what xellter sends: no change in the client at all.

## Decision
- **xellter answers `GET /v1/usage` in kenari's shape.** The body has `object`, `start`, `end`,
  `data` (one row per model and billing kind) and `total`, with the same field names and the same
  units as kenari: token counts, `cost_micro_idr` (what was paid) and `catalog_micro_idr` (the
  list price of the same traffic). `range` accepts `24h` (since midnight) and `30d`; anything else
  is a 400.
- **The rows come from xellter's own request log, not from kenari.** xellter knows the real model
  every request was routed to; kenari would only report what its own keys saw, which is the same
  traffic but without xellter's view of the routing. A failed attempt is not counted: it never
  reached the model.
- **The rows are scoped to the client.** A client only sees its own traffic, the same way the
  proxy scopes every other call to the key that authenticated it.
- **`GET /v1/account/balance` and `GET /v1/account/quota` are proxied from the account state
  xellter already keeps.** xellter reads both from kenari with the unrestricted account key every
  five minutes, so it answers them from its own state instead of asking kenari on every call.
- **Nothing else changes.** The proxy still owns the model: the client asks for a preset and
  xellter decides the real model, the ranking, the failover and the accounting. The usage
  endpoint reports what xellter decided, not what the client asked for.

## Consequences
- A client that reads `/v1/usage` works against xellter unchanged: the same URL, the same JSON,
  the same units. Pointing it at xellter instead of kenari costs it nothing.
- The per-model view in the client shows the real models again, with the requests, the tokens and the
  price of each one, split into free, plan and pay-as-you-go.
- xellter's own numbers and kenari's may differ slightly: xellter counts the traffic it routed,
  kenari counts the traffic its key sent. A request that failed after reaching kenari is in both;
  one that failed before is in neither.
- The catalog price is the price at the time of the request, not today's price. A model whose
  price changed during the window is valued at the price it had then.
