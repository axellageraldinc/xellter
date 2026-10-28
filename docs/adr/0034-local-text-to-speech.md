# ADR 0034 — A local model can also speak

- Status: Accepted
- Date: 2026-10-28
- Deciders: maintainer
- Amends: ADR 0033 (local models)

## Context

ADR 0033 let a model on this machine serve speech-to-text. The same box now runs `tts-local`, an OpenAI-compatible
`/v1/audio/speech` server for Bahasa Indonesia with voice cloning (Pocket TTS, ONNX int8, CPU; `~/apps/tts-local`).
The routing, key-less serving, fallback and accounting of ADR 0033 do not depend on the kind of audio; only the
config validation allowed `audio_transcription` alone.

## Decision

1. **`audio_speech` is a local category** next to `audio_transcription` (`config.LocalCategories`). Nothing else
   changes: the model joins the pool of every `audio_speech` preset as a free model, is called without a kenari key,
   falls through to the next model (kenari's) when it errors, and shows in usage as `local` / `free`.
2. **The voice is the caller's.** The request body is forwarded as it is apart from `model`, which becomes the
   local server's own name, so `voice` reaches the local server untouched. Voices are managed on the local server
   (`GET/POST/DELETE /v1/voices` on `127.0.0.1:8491`), not through xellter: it carries only `/v1/audio/speech`.

## Consequences

- With the local model in the pool, a `cheapest` TTS preset prefers it (free beats priced). A kenari voice name sent
  as `voice` will not exist locally: the local server answers 400, which is retried on the next model, kenari's.
- A client that wants only the local voice should use a preset whose pool holds nothing else and a `voice` the
  local server knows (`id-1`..`id-4` ship with it).
