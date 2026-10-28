# ADR 0031 — The size of a request is estimated from its content, and corrected by what the upstream reports

- Status: Accepted
- Date: 2026-10-28
- Deciders: maintainer
- Amends: ADR 0026 (the context estimate), ADR 0029 (the first-token timeout)

## Context

The gateway needs to know roughly how big a request is: to put the models whose window can hold it first
(ADR 0026), and to give a long prompt more time before its first token (ADR 0029). It used four bytes per
token over the raw body. That is right for English prose and wrong for most of what a request body holds:

- an inline image is a megabyte of base64 and costs about a thousand tokens, so a request with one looked
  250 times too large;
- Chinese, Japanese and Korean text costs about one token per character, not a quarter;
- JSON punctuation and key names are counted as if they were prose.

## Decision

1. **Estimate from the JSON.** The body is read token by token. A string value is estimated from its
   characters (a quarter of a token per ASCII character, half for accented Latin, one for CJK, two for an
   emoji, about 0.6 for other scripts). A key name is one token. An inline image or file (a `data:` URL, or
   a long base64 string) is a flat 1,100 tokens whatever its size.
2. **Correct it with what the upstream reports.** After every answer the gateway compares its estimate with
   the prompt tokens the upstream reported and keeps a moving average of that ratio per client (clamped to
   0.25..4, ignored for requests under 400 estimated tokens). A client whose traffic is mostly code, or
   mostly one language, or mostly tool output, ends up with its own correction.
3. The estimate is still only used to order models and to scale a timeout. It never refuses a request.

## Consequences

- Image-heavy requests are no longer treated as huge, so they are not pushed off the models that can serve them
  and their timeout does not jump to the 60-second ceiling.
- The correction starts at 1.0 and learns from the first sizeable answers; it is not saved, so a restart
  starts again at 1.0 and relearns within a few requests.
- It is an estimate. A model's tokenizer can differ from the average by tens of percent.
