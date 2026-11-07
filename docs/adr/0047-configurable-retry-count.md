# 0047 — The retry count is configurable, and is 0 by default

Status: accepted

## Context
A retriable failure (5xx, timeout, 429, network error) was retried once on the same model and key before the
next model was tried. That count was fixed in the proxy. A retry costs the caller the wait of the failed call
again, and a model that just failed is usually still failing.

## Decision
`routing.retries` (0 to 5, default 0) is how many times a retriable failure is tried again on the same model
before the next model is tried. With 0 the first failure moves on at once. A non-retriable error, an empty
answer, a missing first token and a caller that left are never retried, whatever the count.

Health judgement (eviction, p99 slowness, failures in a row) is unchanged: every failed attempt is still
recorded.

## Consequences
- With no retry and no other usable model in the preset, the caller gets the upstream's own error after one call.
- The last model that can still be tried is never timed out on first token, so a slow-but-working last model
  still answers.
