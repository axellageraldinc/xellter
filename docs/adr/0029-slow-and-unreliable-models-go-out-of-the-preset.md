# ADR 0029 — A slow or unreliable model goes out of the preset

- Status: Accepted
- Date: 2026-10-28
- Deciders: maintainer
- Amends: ADR 0020 (eviction)

## Context

ADR 0020 took a model out of a preset after three errors in a row. Nothing looked at
speed, and a model that failed one request in four never had three in a row.

Measured on four days of real traffic, the slow answers came from free models that queue
for 10 to 90 seconds before the first token (a median of 33s for one, 21s with 25% failures
for another), not from the reasoning effort (ADR 0028). Because a session stays on the model
it started with (ADR 0025), a conversation that landed on one of them stayed slow, and a
streamed answer whose headers arrived at once but whose first token did not could not be
failed over: the response was already committed to the caller.

## Decision

1. **A model is judged on its own record in each preset.** Per client and preset, over the
   eviction window (60 minutes), once it has `health_min_requests` (6) requests:
   - **slow**: the median wait for the first token of its streamed chat answers is above
     `slow_first_token_s` (10);
   - **unreliable**: `fail_percent` (40) or more of its requests failed. A failure is a 5xx, a
     network error or a first-token timeout. A 4xx and a caller that went away are not the
     model's fault.

   A model that is judged slow or unreliable is out of that preset only. The ring skips it and
   the next best model of the pool takes its place, as it does for an eviction after errors.
   The scores are not recomputed: they do not depend on who is out.
2. **It comes back by itself, and is judged again.** The first time out lasts
   `quarantine_minutes` (30). When the time is up the model serves again with a clean record
   and is judged on half as many requests (3). If it is slow or unreliable again, it goes out
   for twice as long (60, 120, ...) up to `evict_cooldown_minutes` (720). When it passes a judgement
   the next time out starts from 30 minutes again.
3. **A preset is never emptied.** A model is not taken out if fewer than two other models of the
   preset would remain in service.
4. **A session leaves a slow model.** If the model a session is pinned to waits longer than
   `repin_first_token_s` (15) for two of the session's last three requests, the pin is dropped
   and the model goes to the end of that session's order. The session pins the next model that
   answers. (Two of three, because a quick side call between two slow answers must not hide the
   problem.)
5. **A silent stream fails over.** For a streamed chat request the gateway reads the first bytes
   of the answer before it commits to it. If nothing arrives within `first_token_timeout_s` (15,
   plus one second per 10,000 tokens of prompt, at most 60) the request is cancelled and the next
   model is tried; the timeout counts as a failed request in the model's record. The last model
   that can still be tried is never timed out. `-1` turns the timeout off.
6. **The panel says who is out and why.** Each preset lists the models that are out, the
   reason with the numbers (for example `slow: median first token 12.3s over its last 7
   requests (limit 10s)`), when they come back and how many times in a row they went out. The same line
   goes to the log.

## Alternatives considered

- **Rank by latency.** The ranking rotates over the models it keeps; sorting by speed would send
  all traffic to the fastest and make it the next one to queue. Taking the slow ones out keeps the
  rotation and the spread.
- **A global record per model.** One client's large prompts would take a model out for another.
- **A fixed timeout without a record.** A slow model would be tried first and given up on by every
  session, forever.

## Consequences

- A model that is only slow for large prompts can be taken out of a client that sends them. That is
  the intended reading: it is slow for that client.
- A model that is out for slowness costs nothing while it is out, and one test period when it returns.
- The model of a long conversation can change when it keeps being slow. That loses the provider's
  cache for that conversation, which is cheaper than a conversation that waits 20 seconds per turn.
