# ADR 0027 — What the gateway learned, it keeps

- Status: Accepted
- Date: 2026-10-28
- Deciders: maintainer
- Amends: ADR 0026 (a reasoning effort is a promise the gateway keeps)

## Context

ADR 0026 made the gateway keep the reasoning-effort promise by **measuring** what
each model does with the effort, because no catalog field predicts it. The
measurement was kept in memory: the registry that holds it was built from the
traffic the gateway serves, and it was thrown away when the process ended.

The consequence was small and real. Every restart of the gateway made it blind
again. A model that ignores "none" was served such a request as asked, because
there was no evidence against it, and the client waited for an answer that had
been thought out — the exact failure the effort was meant to prevent.

It was not a correctness bug: the gateway still answered, still routed, and still
learned. It cost the client a few slow answers after every restart, and restarts
happen on every upgrade and every crash.

Two kinds of fact were being thrown away, and they do not have the same lifetime:

- **Honours / ignores** — a measurement of a model's behaviour. A provider can
  ship an update that changes it, so the fact is only true until it is measured
  again.
- **Rejects** — a model answers HTTP 400 to the reasoning field. That is a
  property of the API's parameter validation, not a passing mood. It holds.

## Decision

**The facts are written to the database and read back on the next start.**

- Two tables: `effort_facts` (one measured fact per model and level, with the
  time it was made) and `effort_thinkers` (the models seen producing reasoning
  at all).
- The thinkers travel with the facts because a fact is only as good as the
  evidence behind it. "Honours" means the model was seen thinking once and then
  stopped; without the record that it ever thinks, a restored "honours" cannot
  survive the next quiet answer.
- The facts are written **on a timer and on shutdown**, never from the request
  path. The request path does not write to the database synchronously.
- A fact older than **30 days** is dropped when it is read back, and the model is
  measured again the next time it is served. The trust window is long enough that
  a restart never costs the client a slow answer, and short enough that a
  provider that changed a model's behaviour is not pinned forever.
- **A refusal is exempt from the trust window.** It holds however old it is.
- The same rule governs the routing: a fact that is too old no longer steers the
  order, so a model that stopped honouring the effort is preferred again.

- The write is one transaction, so a crash cannot leave half the knowledge behind.
  A gateway that came back with half its facts would serve some models as if it had
  never met them.
- The facts are written **only when the registry has changed**. A process that
  learned nothing — a read-only command such as `xellter explain`, which loads
  the facts over the same data directory and closes again — leaves the file alone
  instead of writing back an older copy of what the running gateway has learned
  since.

## Alternatives considered

- **Keep it in memory and accept the cost.** The current behaviour, and safe. A
  restart costs the client a handful of slow answers, and the gateway recovers by
  itself. Chosen against because the fix is small and the cost is paid by the
  client, not by the gateway.
- **Probe every model at startup.** It would cost a call per model per restart
  and needs a prompt that reliably induces reasoning. The traffic the gateway
  already serves answers the same question for free.
- **Save the facts with no expiry.** Simpler, and wrong: a provider that changes
  a model's behaviour would leave the gateway preferring a model that no longer
  behaves that way, indefinitely.
- **Save the facts as a config file the owner edits.** The owner would have to
  learn which models think, and the answer changes when a provider ships an
  update.
- **Store the facts in the request log** and derive them on startup. The log is
  append-only and is pruned on a retention window, so the facts would come back
  incomplete and would have to be re-derived from a table that is meant to be
  thrown away.

## Consequences

- A restart no longer costs the client the knowledge the gateway had already paid
  for: a model that ignores the effort is demoted again at once.
- The database holds a little more state. It is the same file as the request log
  and the leaderboard scores, and it is covered by the same daily backup.
- A fact can go stale. It is dropped after 30 days and the model is measured
  again, so the cost of a stale fact is one request, for one model, once.
- The registry has a version counter, so a saver can tell whether there is
  anything new to write.
- `Store.Close` is now safe to call twice. The shutdown path and a test's
  cleanup can both reach it, and closing a closed channel is a panic that takes
  the process down.
