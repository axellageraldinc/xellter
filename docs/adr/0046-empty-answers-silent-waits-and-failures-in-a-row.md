# ADR 0046 — Empty answers, waits nobody saw, and failures in a row

- Status: Accepted
- Date: 2026-11-04
- Deciders: maintainer
- Amends: ADR 0020 (eviction), ADR 0029 (slow and unreliable models)

## Context

A day of aixell traffic showed three kinds of bad model that the rules did not catch.

1. **Empty 200s.** `crouton-alpha:free` answered non-streamed requests with status 200 and no text. The caller
   cannot use that, but the gateway logged a success, pinned the session to the model and never failed over.
2. **Slow models that stayed in.** `llama-3-3-70b-instruct:free` averaged 24 s (max 179 s) and several others 10 to
   20 s, and none was taken out. Two causes, neither of them an exemption for free models (ADR 0022 only adds models
   to a pool; `Observe` never looks at price):
   - Only a *streamed* chat answer produced a wait. A non-streamed answer was recorded with no wait at all, so
     the median of its waits was empty and the model could never be judged slow.
   - The slow and failure record was kept for `evict_window_minutes`. The live server sets that to 1 minute for the
     three-errors rule, so the record held at most the last minute of requests and never reached
     `health_min_requests`.
   - A caller with a 15 s timeout hangs up on a slow model before it answers; that is not recorded at all.
3. **A model that always fails.** `clef-omni:free` (503) and `clairic:free` (400) failed every call while failover
   hid it. A 4xx is deliberately not the model's fault (ADR 0029), and the 503s came about once every quarter of an
   hour, never three inside a minute.

## Decision

1. **An empty answer is a failed attempt.**
   - A non-streamed `/v1/chat/completions` answer with status 2xx is read in full (up to 8 MB, a bigger body is
     passed on untouched). If every choice has no text, no tool call, no function call and no audio, the attempt
     fails with the reason `empty answer`: the request goes to the next model at once (no retry on the same one),
     and the model is counted. Whitespace is not text; a reasoning trace with no final text is empty.
   - It counts in the model's record (share of failed requests), in the three-errors rule and in the run of
     failures in a row (4). Three empty answers inside the eviction window take the model out with the reason
     `3 errors within N minutes, the last: empty answer`.
   - When no other model can be tried, the empty answer is relayed as before: something has to answer.
   - A **stream** that ends with no text and no tool call cannot fail over, because it is already with the caller.
     It is counted in the record and the run of failures, the session's pin is dropped, and the row in the request
     log carries `empty answer`. It is judged only when the whole stream fits in the 16 KB tail kept for usage
     accounting (an empty stream is a few hundred bytes); a longer stream is never empty. Streams are not counted
     in the three-errors rule: the call has already cleared that record for answering.
   - Only `/v1/chat/completions` is inspected. `/v1/responses` and `/v1/messages` have other shapes and are left alone.
2. **A non-streamed answer has a wait.** It is the time to the whole answer, less 20 ms per output token, floored at
   1 ms. What is left is the model queueing, and it is judged with the same `slow_first_token_s` as a stream's first
   token. A model that writes a long answer is not slow for writing it.
3. **A caller that hangs up is a wait.** If the caller cancels after at least `slow_first_token_s` seconds, with no
   byte received, that wait is recorded for the model as a slow sample. It is not a failure: no run of failures and no
   failure share builds up from it. (A cancelled caller after a shorter wait is still ignored.)
4. **A run of failures in a row takes a model out, however far apart they come.** Five failed requests in a row with
   no answer in between (three on probation, like the other judgements) take the model out, with the reason
   `failing: its last 5 requests in a row all failed`. A failure here is the same as for the failure share (5xx,
   network error, timeout) plus an empty answer plus a **confirmed refusal**: a 400, 404 or 422 counts only once
   another model has answered the same request, because then the request was good and the model was not. A request
   that every model refuses counts against nobody. A run older than 24 hours is forgotten. The usual protection
   applies: a preset is never emptied (two models stay in service).
5. **The health record has its own window.** `routing.health_window_minutes` (60) is how far back the slow and failure
   share judgements look. `evict_window_minutes` times only the three-errors rule, as ADR 0020 intended, so setting
   it to one minute no longer shrinks the other record.

## Alternatives considered

- **Exempt free models, or not.** There is no exemption to narrow; the cause was the missing wait and the window.
- **Judge a non-streamed answer by its whole latency.** A model that writes 4,000 tokens would be slow. The per-token
  allowance separates writing from queueing without a second threshold.
- **Count every 4xx against the model.** One malformed request would take models out of every preset it passes.
  Waiting for another model to answer is the evidence that the request was fine.
- **Retry an empty answer on the same model.** An empty 200 from a model is repeated more often than not, and the
  caller is waiting.

## Consequences

- A model that returns nothing, queues for the caller or refuses every call goes out within a few calls and comes
  back on the usual doubling cooldown (ADR 0029).
- A model whose answers are legitimately empty for some use (a tool-only call that returns no content and no tool
  call) would be taken out. Such an answer is useless to a chat caller; the cost is accepted.
- A non-streamed answer is buffered whole before it is relayed (up to 8 MB). Chat answers are small, and the
  request already waits for the whole answer from the upstream.
- No config key changes on the live server: `health_window_minutes` defaults to 60.
