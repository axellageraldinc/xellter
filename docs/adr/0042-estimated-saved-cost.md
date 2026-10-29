# ADR 0042 — Estimated saved cost against popular models

- Status: Accepted
- Date: 2026-10-29
- Deciders: maintainer
- Builds on: ADR 0005 (RTK), ADR 0012 (the panel shows only what matters), ADR 0031 (token estimates)

## Context

The Usage page shows what was spent and how many characters RTK removed, but not whether routing and RTK were worth
it. The owner wants one figure per client, preset and globally: what would this traffic have cost on a model people
usually pick, and how much of that did xellter not spend.

## Decision

1. **The comparison.** For a spend: `baseline = (uncached input + RTK-removed tokens) x input price + cached input x
   cache price + output x output price`, at the compared model's list price. `saved = baseline - charged`, shown in
   rupiah and as a percentage of the baseline. The RTK part (removed tokens at the input price) is shown on its own.
   RTK reports characters; they are turned into tokens at four per token (ADR 0031).
2. **Which models.** `usage.compare_models` in the config file, default `claude-sonnet-5-5`, `deepseek-v4-1-flash`,
   `minimax-m3-1-flash`. The Usage page can add any other catalog model for the current view (kept in the link as
   `?compare=`) without touching the config. No panel control edits the config key (ADR 0012, 0014).
3. **Non-text models.** The log now records `unit` and `units` for every non-chat call (images made, seconds of
   video or audio, thousands of characters spoken, songs), because rupiah alone cannot be turned into units for a
   free model. A compared model answers only for the kinds of work it can do (its token price, and each catalog
   unit line), and the charged side counts only that same work. A video is priced on the model's cheapest line of the
   unit. X search and System One are services, not models, and are never compared (System One's tokens and cost leave the text side). Rows logged before this change have no units:
   an image, a song or a transcription is counted the way the gateway bills a request that names no amount (1 image,
   1 song, 60 s), speech and video are not priced.
4. **Honest limits.** It is an estimate: it assumes the compared model would have used the same number of tokens
   and answered as well, and it uses list prices, as the charged side does.
