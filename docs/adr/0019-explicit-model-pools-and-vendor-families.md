# 0019 — Model pools are explicit; a family is a vendor

- Status: Accepted
- Date: 2026-10-26
- Deciders: owner (Axell)
- Supersedes: the "generated presets" part of ADR 0008, the allow/deny rules of ADR 0012

## Context
Presets were generated: xellter looked at the models a client's keys could call and created one preset per
category by itself. The owner could edit them but not meaningfully own them, and there was no way to say
"this preset may use these models and not those" except through globs and capability gates.

## Decision
**A preset's pool is an explicit list of model ids.** The owner ticks models in the panel; the optimization
only orders what is ticked. Nothing is generated, nothing is added, nothing is removed.

- **One preset per category per client.** The preset name is derived from the category (`text`, `image`,
  `video`, `tts`, `stt`, `music`, `systemone`), so a second text preset cannot be expressed and is refused
  by validation. Apps can rely on the names.
- **A client may name a model that is in one of its own pools** — that is how a caller pins a concrete
  model. The old `passthrough` glob list is gone; being in a pool is the allow list.
- **A family is a vendor.** The panel groups the model picker by kenari's `owned_by` (google, openai,
  anthropic, tencent, …) and a family checkbox selects every model of that vendor in that category. Vendor
  is authoritative and matches how the owner thinks about models; a name prefix would split Tencent's
  `hy3` from its `hunyuan-*` models.
- **Dedup is by model id, not by key.** kenari prices per model, so the same model reachable through two
  keys is one candidate. The key is chosen per request by billing class (free, then plan-covered, then
  balance), which is where the keys actually differ.
- **System One is the sixth category.** Its models live on kenari's own catalog endpoint
  (`/v1/models?modality=systemone`), are fetched and priced like any other, and never enter a text pool.

## Alternatives considered
- **Keep generating presets and let the owner edit them.** A generated pool guesses at both membership and
  intent, and every catalog change silently reshapes it.
- **Family by name prefix.** Cheap, but wrong: it splits vendors whose product names do not share a prefix
  and merges unrelated vendors that do.
- **Keep `passthrough` globs alongside pools.** Two ways to grant access is one too many.

## Consequences
- A new client starts with no presets; the owner creates one per category and picks its models. Until then
  the client authenticates but has nothing to route to (404 with a reason).
- The pool is a hard boundary: a model outside it cannot be reached by name either.
- A catalog id that disappears from kenari is rejected with "not in the kenari catalog" rather than
  silently dropped from the list.
