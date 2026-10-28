# ADR 0030 — A conversation without a session id is told apart by its first user message

- Status: Accepted
- Date: 2026-10-28
- Deciders: maintainer
- Amends: ADR 0020 (how a session is identified)

## Context

A client that sends no session header, `prompt_cache_key` or `user` was identified by a hash of the
first 600 bytes of its `messages`. For an agent whose system prompt is longer than that (Polifin's is),
every conversation, side call and job of the agent hashed to the same value. In four days of traffic one
client had a single "session" of 87 requests with prompts from 11k to 150k tokens, all on one model.

One pin for everything means no spread across the pool, and one slow model slows every conversation of that
client. It also made the per-session rules of ADR 0029 meaningless, because they judged a mix of conversations.

## Decision

Such a request is identified by a hash of its first user message, which every later turn repeats, and
which differs between conversations. The system prompt is not part of the key. A request with no user
message falls back to the old key. A conversation keeps its model as it grows; two conversations of one
agent can sit on two models.

## Consequences

- The pins of such clients are keyed differently, so each conversation is pinned again once after the
  deploy.
- A client that compresses its history so that the first user message changes starts a new session at that
  moment and may move to another model. A client that wants to avoid that sends a session id.
