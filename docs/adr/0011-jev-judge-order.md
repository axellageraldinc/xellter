# 0011 — The JEV judge is an ordered list, not one model

- Status: Accepted
- Date: 2026-10-24
- Deciders: maintainer

## Context
Judging already ran as a chain: the configured judge, then every other free System One model in turn, so one judge being down at kenari never stopped a round. The panel only exposed the head of that chain as a dropdown ("First judge to try"), which hid the rest of the order: the owner could not see which judge would take over, could not put a second choice in front of an automatic one, and could not take a paid judge out of the middle of the chain. The UI suggested a single choice while the behaviour was a sequence.

## Decision
`jev.judges` is an ordered list of System One model ids. The panel shows it as a numbered list with move up / move down / remove and an "Add" picker over the System One models in the catalog, plus the chain that is in effect right now (configured judges in order, then the automatic free tail marked as such).

- The judges in the list are tried top to bottom. A configured judge is used even when it is not free, because the owner put it there on purpose; the automatic tail only appends free judges.
- After the configured list, any other free System One judge is still appended, so a round survives an outage without configuration.
- `jev.model` (the old single-judge key) is still read and moved to the front of `judges` on load. It is never written back, so the config file converges on the new shape.
- Validation rejects empty and repeated ids; an empty list falls back to `jev-1-13:free`.

## Alternatives considered
- Keep the dropdown and document the hidden order: the owner keeps guessing what happens after the first judge fails.
- One judge only, no fallback: a single 5xx at kenari would block judging for the whole round (the previous behaviour before the chain existed).
- A drag-and-drop editor: nicer, but the panel has no build step and no drag library; move up/down buttons work on mobile too.

## Consequences
- The panel and the runtime agree on one order, and the panel can show the effective chain.
- A paid judge placed in the list spends the daily JEV budget; the ordering hint says so.
- Older configs keep working unchanged; the next save rewrites them in the new shape.
