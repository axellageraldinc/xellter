# 0018 — A client purpose changes how its text preset is scored

- Status: Accepted
- Date: 2026-10-26
- Deciders: maintainer

## Context
One text preset per client means one quality signal, but the clients do different work: a coding agent and
a chat assistant do not want the same model. The owner asked for a purpose on the client that changes the
scoring, and for no human preference rating typed by the owner.

The Arena publishes more than one leaderboard, which is what makes this possible without inventing numbers.

## Decision
A client has a **purpose**: `general`, `coding`, `agentic`, `vision` or `bulk`. It reorders the preset's
pool; it never changes which models are in it.

A category other than text has exactly one metric of its own and the purpose never changes it: an image
preset is ranked on the image arena whoever owns it.

| Purpose | Sources (fixed weights) | Coverage measured 2026-10-26 |
|---|---|---|
| general | Arena `text` | 48 of 77 text models |
| coding | SWE-bench Verified 50 %, Arena `webdev` 30 %, Aider polyglot 20 % | 41 webdev; 13 SWE-bench entries; Aider reachable |
| agentic | Arena `agent` | 29 of 77 |
| vision | Arena `vision` 50 %, Arena `document` 50 % | 29 and 9 |
| bulk | none: cost only, Best Quality not offered | 77 of 77 |

- **One number per purpose.** Sub-scores are never mixed inside a source: the Arena leaderboard's own
  "overall" category is used, not its per-task rows.
- **Missing sources renormalise.** A coding model with two of the three sources is scored on those two,
  and the panel says which were used.
- **Fallback, never exclusion.** A text model with no purpose-specific score is scored on the general
  arena and flagged `general score`. A model with no evidence anywhere is `unranked`: excluded from Best
  Quality, still competing on Cheapest and Balanced.
- **Purpose covers text only.** Every other category reads its own single source: image → Arena
  `text_to_image`, video → Arena `text_to_video`, STT → the ASR leaderboard, TTS / music / System One →
  nothing (cost only, marked `unranked`).
- **The price-percentile proxy is gone.** It pretended to be quality. `unranked` is the honest replacement.
- **The owner types no numbers.** Every source is fetched and matched onto catalog ids by name.

## Alternatives considered
- **A quality score per model typed in the panel.** The owner explicitly does not want to maintain one,
  and it goes stale as models arrive.
- **Strict purpose scoring.** A model without a purpose-specific score would be unusable; on the agentic
  purpose that is 48 of 77 models silently dropped. Fallback with a visible label is the safer default.
- **LiveBench** (the source with per-task sub-scores such as agentic coding and data analysis) publishes no
  machine-readable data: every data path returns the single-page app's HTML. Not usable without a
  rendered browser, so it is not used.

## Consequences
- Two clients with different purposes get genuinely different orders for the same pool.
- TTS, music and System One are price-only and labelled `unranked`; that is a fact about public
  benchmarks, not a gap in xellter.
- Curating a purpose's coverage is now a benchmark-matching problem (names, aliases), not a config knob.
