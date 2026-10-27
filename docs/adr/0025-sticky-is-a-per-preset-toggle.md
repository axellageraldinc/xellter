# ADR 0025 — Stickiness is a per-preset toggle

- Status: Accepted
- Date: 2026-10-27
- Deciders: maintainer
- Amends: ADR 0020 (the session pin)

## Context

ADR 0020 pinned every session to one model, for the life of the session, and the
pin was a single global switch: `routing.sticky_session`.

The switch answered one question for every client and every preset at once:
does a session keep its model? But the answer is not the same everywhere.

A chat preset that serves a coding agent wants the pin: the provider's prompt
cache is warm on the same model, and a conversation that changes model halfway
through loses that. A bulk preset that fires the same question at a hundred
different providers wants the opposite: it wants the traffic spread over the
whole pool, because one model behind one key is a rate limit waiting to happen.

The global switch forces one answer for both. Turning it off hurts the agent;
turning it on hurts the batch.

## Decision

**Stickiness is a property of the preset, not of the gateway.**

- Every preset carries a `sticky` flag. A preset that does not mention it
  **sticks**, exactly as it behaved before: an existing config file loads
  unchanged and keeps the behaviour the owner already had.
- A sticky preset pins a session to one model for the life of the session.
  A preset with `sticky: false` keeps no pin at all: every turn is routed
  again, and the round-robin ring moves on.
- The flag is per preset, so one client may hold a sticky chat preset and a
  roaming bulk preset at the same time.
- The old global knob `routing.sticky_session` is **refused** on load, with a
  message that names the new place. A file that still carries it would
  otherwise be silently ignored and look like it still worked.
- The panel shows the toggle on the preset card, next to the optimization and
  the pool. It is the only place the flag is edited.

The rest of ADR 0020 stands: eviction still beats the pin, a retriable failure
still retries the same model once, and the pin still has no expiry.

## Alternatives considered

- **A global switch plus a per-client override.** Two places to look for one
  decision, and the override silently wins for a reason nobody remembers.
- **A sticky duration instead of a boolean.** ADR 0020 already rejected an
  expiry: a session that silently changes model after 30 minutes defeats the
  point. The real question was never "how long", it was "at all".
- **A separate preset for the same category.** A client holds one preset per
  category (ADR 0021), so this would have meant two presets of one kind
  serving the same app.

## Consequences

- `routing` has six knobs again, and the seventh is on the preset, where the
  rest of the preset's behaviour lives.
- A config written before this ADR loads unchanged: every preset that does
  not mention `sticky` sticks.
- A file that carries the old `routing.sticky_session` fails to load with a
  message naming ADR 0025, like every other retired knob.
- The stickiness of every preset is cached by the router with the rankings, so
  the request path only reads it.
- A preset that roams keeps no pin, so `/v1/session` reports the last model it
  served rather than a pinned one.
