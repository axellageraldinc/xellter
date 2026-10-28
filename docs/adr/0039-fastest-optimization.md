# ADR 0039 — A fourth optimization: Fastest

- Status: Accepted
- Date: 2026-10-28
- Deciders: maintainer
- Amends: ADR 0017 (the three optimizations), ADR 0032 (speed breaks ties)

## Context

ADR 0032 made speed the tie-breaker: price (Cheapest) or score (the other two) decide, and models that tie are ordered
by how fast they answer. That is not enough for an app that needs answers quickly and does not mind paying for them or
giving up some quality, such as a spoken conversation where someone is waiting to hear the reply (ADR 0038). Among free
models every price ties, so speed already decides there, but a faster paid model could never rank above a slower free one.

## Decision

1. **`fastest` is a fourth optimization.** The order is the speed class first (the measured wait for the first token of
   the model's streamed answers, failures counted, in 1.5 second classes, exactly as ADR 0032 measures it), then price,
   then the leaderboard score, then the name.
2. **A model nobody has measured counts as average**, the median wait of the measured models in the pool (ADR 0032, rule 3).
   It is neither favoured nor punished, still receives traffic, and is measured by it. There are **no periodic test
   requests**: probing would spend the owner's usage limit, and the ranking is allowed to be less accurate instead.
3. The health rules (ADR 0029) still take a model out of a preset when it proves slow or unreliable, and free and
   discounted models still join every preset of their category (ADR 0022).

## Consequences

- A preset can now put a fast, paid model above a slow, free one. Only the owner chooses that, per preset.
- A new model gets measured only by real traffic, so a fast newcomer can take a while to rise. The round-robin over the
  top list (ADR 0020) is what gives it that traffic.
- Fastest ignores quality except to break a tie. Keep the pool to models that can do the work (tool calls, for an agent).
