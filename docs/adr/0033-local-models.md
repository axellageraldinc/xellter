# ADR 0033 — A model can run on this machine instead of at kenari

- Status: Accepted
- Date: 2026-10-28
- Deciders: maintainer
- Amends: ADR 0022 (free models are always in the pool)

## Context

Speech-to-text for the aixell client went to a remote whisper model at kenari. On this machine a local model is good
enough for Indonesian (maleo-ai/whisper-small-id, int8 on the CPU: 8% WER at 0.75 real-time on the 4-core server),
costs nothing and works without the network. xellter could only route to kenari: every attempt carried a kenari key,
the catalog came from kenari alone, and the upstream address was `kenari.base_url`.

## Decision

1. **`local_models:`** in `xellter.yaml` lists models served by an OpenAI-compatible HTTP server on this machine:
   `id` (the name presets and the panel use), `category` (only `audio_transcription` for now), `base_url`, and an
   optional `model` (the name that server knows; default the id). Config is the only place: the panel gets no control
   for it (ADR 0012), it only shows the model in the picker, under the owner `local`.
2. **They join the catalog** as free models of their category and so, like every free model, are in the pool of every
   preset of that category automatically (ADR 0022). A kenari model with the same id is never shadowed.
3. **They are served without a kenari key.** Attempts on a local model carry one shared stand-in key with no secret and no
   kenari account, so kenari's rpm, quota, cooldown and breaker never apply, and no kenari credential is sent to the
   local server. A client without any kenari key can still use them.
4. **Failure is the router's business.** A local server that errors is retried like any model and, if it keeps failing,
   is taken out of the preset by the usual eviction and health tracking (ADR 0020, 0029); the request falls through to
   the next model, kenari's included. A local 5xx is retriable and never a verdict on a key.
5. **Usage counts them.** The request log records a local request like any other (model = the local id, upstream
   `local`, billing `free`), so the panel's usage drill-down and the client endpoints `/v1/usage` and
   `/v1/usage/models` list the local model next to kenari's. The panel shows it as free even when the ranking no
   longer holds it.
6. **Hot reload.** A change to `local_models` refreshes the catalog at once instead of at the next sync.

## Consequences

- With the local model in the pool, a `cheapest` STT preset prefers it (free beats priced). Take it out of a preset's
  reach by removing it from `local_models`; there is no per-preset opt-out.
- The model server is a separate process with its own unit; xellter neither starts nor supervises it.
- A local model has no benchmark score: it is `unranked`, which only excludes it from Best Quality.
