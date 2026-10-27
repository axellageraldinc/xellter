# ADR 0026 — A reasoning effort is a promise the gateway keeps

- Status: Accepted
- Date: 2026-10-27
- Deciders: maintainer
- Amends: ADR 0017 (the gateway is transparent to the request)

## Context

A client asks for a reasoning effort. `reasoning_effort: "none"` means: answer
without thinking it out first. The owner of a chat app set exactly that, and
the answer came back after nine minutes — 46,773 output tokens for 2,865
characters of text, almost all of it reasoning the reader never saw.

The effort never reached the model. The browser dropped it (the app's own bug,
fixed there), and behind it the gateway forwarded the field verbatim and
assumed the upstream would honour it.

Measured against kenari, that assumption is wrong in three separate ways.
Prompt: "What is 17*23? Answer with just the number."; `rc` is the characters
of reasoning in the response.

| model | rc without the field | rc with `none` | verdict |
|---|---|---|---|
| `minimax-m3` | 8,031 | 0 | honours it |
| `dots-3-note-preview:free` | 15,132 | 0 | honours it |
| `laguna-s-2-1:free` | 16,578 | 0 | honours it |
| `agnes-2-5-flash:free` | 1,777 | 0 | honours it |
| `nemotron-3-5-super-vl:free` | 12,788 | 0 | honours it |
| `beam-1-0:free` | 10,211 | 4,298 | **ignores it** |
| `gpt-oss-120b` | 4,514 | 5,466 | **ignores it** |
| `flint-v1-0:free` | — | HTTP 400 | **rejects it** |
| `nemotron-3-super-120b-a12b:free` | — | HTTP 400 | **rejects it** |

Three findings follow, and each one rules out the obvious fix.

**No catalog field predicts the behaviour.** kenari publishes `reasoning`,
`reasoning_toggle`, `reasoning_options` and `supported_reasoning_levels` for
every model, and xellter decoded none of them. But they do not answer the
question. `agnes-2-5-flash:free` and `beam-1-0:free` have byte-identical
kenari metadata — no `reasoning` flag, no toggle, no levels — and one honours
`none` while the other ignores it. And `nemotron-3-super-120b-a12b:free`
publishes "none" among its supported levels and still answers HTTP 400 to it.

**The alternative wire shapes are not better.** `reasoning: {enabled: false}`,
`thinking: {type: "disabled"}` and `enable_thinking: false` all fail.
`chat_template_kwargs: {enable_thinking: false}` silences
`nemotron-3-super-120b-a12b:free` but makes others think *more*
(`dots-3-note-preview:free` 748 → 914 rc, `laguna-s-2-1:free` 0 → 855 rc).
`reasoning_effort` is the only broadly working lever, so it is the one that
stays.

**The refusal is a parameter refusal, not a request refusal.** `flint-v1-0:free`
answers HTTP 400 with `{"error":{"code":"upstream_rejected","type":"invalid_request_error"}}`
to the literal value `"none"`, and answers 200 to `"low"`, `"medium"` and
`"high"`. The model is not refusing the work; it is refusing the parameter.
Meanwhile the upstream distinguishes a real context overflow
(`context_length_exceeded`) from other 400s, so the two are tellable apart.

There is a second promise in the same request. The client asks for a model
called `xellter`; the gateway answers with a *pool*, and it advertised the
pool's **smallest** context window (128,000, the size of `gpt-oss-120b`) as if
it were the model's. The client believed it, and compressed its conversation at
96,000 tokens — losing 38 messages of history and 66 seconds — to stay inside a
window the model it was actually talking to (1,048,576) never needed.

## Decision

**The gateway keeps the effort promise, and it keeps it by measuring, not by
reading flags.**

- xellter decodes kenari's reasoning metadata (`supported_reasoning_levels`,
  `reasoning_options`, `reasoning_toggle`) into the catalog. It is worth having:
  it tells a client what a model can be asked for, and it is the honest answer
  for the panel. It is **not** used to decide routing, because it does not
  predict the behaviour.
- The gateway keeps a registry of what each model is **known to do** with an
  effort, learned from the responses it has already served. A response that
  carries reasoning is a model that thought; a response without it is a model
  that did not.
- A model is credited with *honouring* an effort only if it was seen thinking
  at some point and then did not think when asked. A model that has never been
  seen thinking is **unknown**, not obedient: it is neither preferred nor
  excluded, because there is no evidence either way.
- A model that answers HTTP 400 to the field is recorded as **rejecting** it.
  That is permanent, and the field is not sent to it again.
- When a request asks for `none`, the models that honour it are tried first.
  The others are kept in the order, not deleted from it: a client is answered
  by something rather than by nothing.
- When an upstream rejects the field, the request is **served again without
  it**. The call succeeds; the client never sees the refusal.
- A graded effort (`low`, `high`) is **not** a routing constraint. A model
  that ignores the grade still answers, and refusing to serve it would be worse
  than the inaccuracy.

For the context window, the gateway advertises the **largest** window in the
pool, not the smallest. A preset is a name that may resolve to any of its
models, and the number it publishes is a promise about the conversation the
client may have. The smallest window is a promise about the worst model in the
pool, and it made the client throw away its history for nothing.

The honest number for a preset is a *range*, and a range is not a number. The
largest window is the one that does not make the client discard context it did
not need to discard: a conversation that outgrows the window of the model that
served it is answered with a context-length error, which the client handles by
compressing, exactly as it should. A window that is too small makes the client
compress early, every time, for no reason.

## Alternatives considered

- **Route on kenari's `supported_reasoning_levels`.** Rejected on the
  measurement: a model that lists "none" still rejects it, and a model that
  lists nothing still honours it.
- **A per-model flag in the config, filled in by the owner.** The owner would
  have to learn which of 120 models think, and the answer changes when a
  provider ships an update. The gateway can see the answer in every response
  it already handles.
- **Probe every model once at startup.** It would cost a call per model per
  restart, needs a prompt that reliably induces reasoning, and a model that
  stays quiet on that prompt looks obedient. The traffic the gateway already
  serves answers the same question for free.
- **Send `chat_template_kwargs` instead of `reasoning_effort`.** Rejected on
  the measurement: it makes some models think more, and it 400s elsewhere.
- **Keep advertising the pool's smallest window.** That is the current
  behaviour, and it cost the owner 38 messages of history and a minute of
  latency on a casual question.
- **Advertise no context window at all.** The client then falls back to its own
  default (256,000 for Hermes), which is a guess, and a wrong guess in the
  other direction. Publishing the pool's largest window is at least the
  gateway's own best knowledge.
- **Give each preset its own window, chosen by the owner.** More knobs, and the
  window of a pool changes when a provider adds a model to it.

## Consequences

- The gateway is no longer transparent to the request body: it may drop the
  reasoning field. This is the point — a promise kept is worth more than a
  body passed through.
- A model that ignores `none` is still served, but later in the order, so a
  preset with only such models still answers.
- The registry is in memory. It starts empty on a restart and refills from
  traffic. A model nobody has called is unknown, and is served as asked.
- The panel can show what each model is known to do with an effort. That
  information is measured, so it is as fresh as the last request.
- A preset now advertises the pool's largest context window. A client that
  fills it and is then served a smaller model gets a context-length error and
  compresses — the correct outcome, arrived at for the right reason.
- The catalog carries kenari's reasoning metadata, so `/v1/models` can tell a
  client which efforts a model supports.
