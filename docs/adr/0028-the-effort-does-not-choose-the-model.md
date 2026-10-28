# ADR 0028 — The reasoning effort does not choose the model

- Status: Accepted
- Date: 2026-10-28
- Deciders: maintainer
- Amends: ADR 0026 (the effort routing), ADR 0025 (the session pin)

## Context

ADR 0026 made "none" a routing constraint: models seen thinking were moved to the
back of the order, and the gateway learned from every response which models those were.

Measured on the real traffic afterwards, it did not pay for itself:

- Most models ignore the effort or refuse it. Of the models the gateway had measured,
  none honoured "none" in the facts it kept; the rest ignored it or answered HTTP 400.
  The routing had almost nothing to prefer.
- It broke stickiness. A client sends side calls (a title, a summary) with "none"
  while the conversation itself asks for something else. The side call was routed away
  from the model the session was pinned to, succeeded elsewhere, and the success
  moved the pin. The next turn of the conversation then followed the pin to a model
  the conversation had never used. The same session changed model several times in a
  few minutes.
- The slow replies were not caused by the effort. In three days of one client's traffic
  the slow answers were reasoning models that think for thousands of tokens whatever
  they are told, free upstreams that queue for 50 to 90 seconds before the first
  token, and 503s that burn 15 to 25 seconds before the next model is tried.

## Decision

- **The effort is forwarded, not routed.** The request keeps its effort, and no model
  is preferred or demoted because of it.
- **A refusal is still honoured.** A model that answered HTTP 400 to the field is
  served without it, and a 400 on a request that carried the field is retried once
  without it (ADR 0026, unchanged). That only prevents failures.
- The gateway no longer learns "honours" and "ignores" from responses. Facts already
  stored are harmless and are not read for routing.
- **The pinned model stays first.** The context-size estimate (four characters per
  token) no longer moves a pinned model behind the others; a model that really cannot
  hold the request answers with a context error and the next one takes it.
- **Pins survive a restart.** They are kept in the database (`kv` row `session_pins`),
  written on the same timer as the learned facts and when the service stops, and
  dropped after seven days without use. A restart used to move every conversation
  to another model.

The advertised context window (largest in the pool) stays as ADR 0026 decided.

## Consequences

- A session keeps one model until that model is evicted or stops answering.
- A client that wants fast answers cannot get them by asking for "none": the gateway
  will not find a model for it. Speed has to come from the pool a preset holds.
