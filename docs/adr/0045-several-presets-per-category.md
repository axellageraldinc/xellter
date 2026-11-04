# ADR 0045 — A client may hold any number of presets of one category

- Status: Accepted
- Date: 2026-11-04
- Deciders: maintainer
- Amends: ADR 0021 and ADR 0038 (one preset per category, except chat)

## Context

ADR 0038 let a client hold several chat presets because one app can need two kinds of answer. The same is true of every
other category: two image pools (cheap drafts, a strong final render), two speech-to-text pools (fast, accurate). The
only workaround was a second client with its own xellter key, which an app that sends one key cannot switch per request.

## Decision

1. **The limit is the name, not the category.** A client may hold any number of presets of any category. A name is
   unique inside a client (a preset is the key of `presets`, so a repeated key in the YAML is a syntax error).
2. **`config.MultiCategories` is gone**, and with it the validation error "a client may hold only one X preset".
3. **A name is never turned into another kind.** Saving over an existing name edits that preset; sending another
   category for it is refused with 409. Renaming onto a taken name was already refused.
4. **The panel offers every category every time** under "Add preset". The only entry that disappears is the fixed-name
   Voice Conversation preset, once the client has it (ADR 0038).

## Consequences

- Nothing changes in routing: a request names a preset, the endpoint is checked against that preset's category, and the
  preset's own ranking, ring, pins, eviction and health records serve it. They were already kept per
  `(client, preset)`.
- Every free and discounted model still joins the pool of every preset of its category (ADR 0022), so two presets of a
  category both receive them.
- An app that asks for a name the client does not hold still gets the "unknown model" answer; there is no fallback to
  "the client's image preset", because there may now be several.
