# ADR 0038 — A client may hold several chat presets

- Status: Accepted
- Date: 2026-10-28
- Deciders: maintainer
- Amends: ADR 0019 and ADR 0021 (one preset per category)

## Context

A preset is the model name an app asks for, and a client held at most one per category. For most categories that
is right: an app has one image pool, one speech pool. Chat is different, because the same app can need two kinds of
answer: a spoken conversation wants a model that starts answering in two or three seconds, while the written chat
can wait for a stronger one. The only way to get two pools was a second client with its own xellter key, and an app
that sends one key per agent cannot switch keys per turn.

## Decision

1. **A client may hold several `chat` presets** (`config.MultiCategories`). Every other category stays at one preset
   per client, and validation still refuses a second one of those.
2. Each chat preset has its own pool, optimization and `sticky` toggle, and its own eviction and health records, which
   were already kept per `(client, preset, model)`. Nothing in routing changes: a request names a preset, and that
   preset's ranking serves it.
3. The panel offers "Add preset" for chat even when the client already has one, and refuses a name the client
   already uses (saving it would replace that preset).

## Consequences

- An app can ask for `voice-conversation` (a pool of fast free models, `sticky: false`, Cheapest so that ties go to
  the measured-faster model, ADR 0032) on spoken turns and for `text` on written ones, with one key.
- Free and promo models still join the pool of every preset of the category, so a voice preset also gets every
  new free chat model; its health rule (a median first token above `slow_first_token_s`) takes slow ones out again.
  Untick the models the voice preset should never use (ADR 0035).
- The pinned-session record is per preset, so a conversation that moves between the two presets is pinned
  separately in each.
