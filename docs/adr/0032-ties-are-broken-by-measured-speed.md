# ADR 0032 — Models that tie on price or score are ordered by how fast they answer

- Status: Accepted
- Date: 2026-10-28
- Deciders: maintainer
- Amends: ADR 0017 (the optimizations), ADR 0020 (the order of the list)

## Context

Every free model costs Rp 0, so "Cheapest" cannot tell them apart. Ties were broken by the model's name,
which is arbitrary: a preset with 18 free models gave its five free slots to the five names that sort first
(`agnes-…`, `beam-…`, `dots-…`, `flint-…`), and a slow model whose name sorted early kept a slot while a fast one
outside the list never got one. The same happens in Balanced and Best Quality whenever two models have the same
score.

## Decision

1. **Speed breaks ties.** After price (Cheapest) or score (the other two), models are ordered by the median wait
   for the first token of their streamed chat answers over the last seven days, raised by their failure rate
   (a model that fails one request in four is as slow as the retry it costs). Then by name.
2. **Measured where the traffic is.** The record of the model in this client's preset is used when it has at least
   five streamed answers, else what every client saw of it, else nothing.
3. **An unmeasured model counts as average.** It takes the median wait of the measured models of the pool, so it
   is neither favoured nor punished, still gets traffic, and gets measured.
4. **Speeds are compared in classes of 1.5 seconds.** Two models within a class are equally fast and fall back to
   the name, so the list does not reshuffle every five minutes over noise.
5. The panel shows the wait that decided on a model ("first token ~3.9s") in its reason.

Price and score still come first: speed never moves a more expensive model above a cheaper one, or a
better one above a worse one. It only chooses between equals.

## Consequences

- The free slots go to the free models that answer fastest in practice, and they change as the traffic changes.
- A model that gets a free slot because it was never measured can be taken out by the health rules (ADR 0029)
  if it proves slow; its record then keeps it out of the next ranking too.
- The ranking reads the request log every rebuild (five minutes) for the last seven days.
