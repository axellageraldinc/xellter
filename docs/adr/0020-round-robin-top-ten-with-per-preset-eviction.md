# 0020 — Round-robin over the top ten, with per-preset eviction

- Status: Accepted
- Amended by ADR 0029 (slow and unreliable models) and ADR 0028 (pins survive a restart).
- Date: 2026-10-26
- Deciders: maintainer
- Supersedes: the sticky/round-robin-band rules of ADR 0003

## Context
Routing had a round-robin band, a rotation counter, sticky sessions with a 30-minute expiry, per-key
cooldowns, a per-key circuit breaker and a max-attempts budget. The interaction between them was the least
predictable part of the system: a bad model could trip the key's breaker and take every healthy model down
with it (observed in a live smoke run), and a "retry" could silently mean "try a different key" instead of
"try again".

## Decision
Routing is four rules, in this order.

1. **Session pin.** A session is identified by `X-Xellter-Session`, `X-Session-Id`,
   `X-Claude-Code-Session-Id`, `prompt_cache_key`, `user`, or a hash of the opening messages. The first
   successful request pins the session to one model; every later request of that session uses it, for the
   life of the session. No mid-session switching.
2. **Round-robin ring.** A fresh session takes the next model in the preset's ring, which runs over the
   whole top list (`routing.top_models`, 10 by default). Every model in the list is used in turn. Equal
   scores are adjacent in the ring, so ties rotate without a band to configure.
3. **Retry once, then move on.** A retriable failure (5xx, timeout, 429, network) retries **the same
   model** once. A second failure, or a non-retriable error, moves to the next model. The retry is a
   property of the model, not of the key.
4. **Eviction.** Three errors for a model inside `evict_window_minutes` (60) take it **out of that preset
   only** for `evict_cooldown_minutes` (720). Eviction beats the session pin: a session pinned to an
   evicted model moves. A success clears the record, so the rule is "three errors in a row inside the
   window", not "three ever".
5. **Reserve.** The ranking reports the pool members the list size pushed out, ordered by the same
   optimization. They sit at the end of the ring, so an evicted model is replaced by the next best one and
   the preset keeps routing against the same number of models.

**A 5xx blocks the model, not the key.** The key's breaker now counts only transport failures, which say
nothing about one model. Letting a single bad model trip the key would take every healthy model down with
it; the preset's eviction rule is what removes a model from service.

**Key health is not configurable.** Key cooldown, breaker and per-model backoff are constants. They protect
the account; they are not routing policy.

Rankings are rebuilt every `rank_refresh_minutes` (5), after every catalog sync, and on demand from the
panel's Recalculate button, which recomputes every preset of one client. Nothing on the request path
recomputes them.

## Addendum (2026-10-28)

The reserve was added to the ring as a whole, so every pool model took fresh sessions in turn and "routes against
10" was not true: a preset with a pool of 23 spread traffic over 23 models, including slow free ones outside the
list. The ring is now the top list only; the reserve replaces a top model that is out, one for one, and otherwise
follows the ring as a fallback.

## Alternatives considered
- **Keep the round-robin band.** It is a second way to express "these are equally good", and the score
  already says that.
- **Sticky sessions with an expiry.** A session that silently changes model after 30 minutes defeats the
  point, which is warm provider caches. Eviction is the only reason to move.
- **Persist evictions in SQLite.** A restart forgiving a model is the safe direction; a model that is
  genuinely broken will be evicted again within three errors.

## Consequences
- Every model in a preset is used, so a preset of three models spreads load three ways by construction.
- A failing model costs at most two attempts before traffic moves on, and disappears from that preset for
  twelve hours after the third error.
- The eviction list is visible in the panel with the moment each model returns.
- Evictions are per preset: a model can be out of `text` and still serving `systemone`.
