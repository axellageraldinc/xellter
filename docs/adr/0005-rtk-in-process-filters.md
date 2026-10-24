# 0005 — RTK as in-process filters on tool results

- Status: Accepted
- Date: 2026-10-24

## Context
Agent traffic is dominated by large tool outputs (diffs, grep results, directory listings, logs) that are re-sent on every turn and billed as input tokens.

## Decision
RTK is a small ordered list of Go `Filter`s (`git_diff`, `grep`, `ls`, `logs`, `truncate`) applied to tool results only (OpenAI `role: tool`, Anthropic `tool_result`, Responses `function_call_output`) before the request leaves xellter. It never touches user or assistant text, only runs on outputs above `min_chars`, fails open (panic or growth keeps the original) and can be switched off globally, per client or per request (`X-Xellter-RTK: off`).

"Plugin" means a Go type implementing `rtk.Filter` registered in `rtk.Filters`; there is no dynamic loading, which keeps the binary static and the hot path predictable.

## Alternatives considered
- External compression proxy (Headroom in 9router): extra hop and process.
- Prompt-level "be terse" injections (Caveman/Ponytail in 9router): change model behaviour; out of scope for v1.

## Consequences
- Savings depend on the agent; the panel shows total characters removed and each request log row shows `rtk -N`.
