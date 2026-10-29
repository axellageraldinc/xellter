# ADR 0040 — An upstream is a provider; kenari is the first one

- Status: Accepted
- Date: 2026-10-29
- Deciders: maintainer
- Amends: ADR 0012 (a key has a name and nothing else), ADR 0033 (local models)

## Context

Everything that talked to an upstream said "kenari": one base URL in the configuration, one catalog source, one
account for every key, `cfg.Kenari` in the proxy, the probe and the plan reader. The routing and the ranking never
cared which upstream served a model, but nothing could say "this model, this key, that upstream". Adding a second
upstream would have meant touching every one of those places at once.

## Decision

1. **A provider is an upstream.** Its id names it. `kenari` is the built-in one and is configured by the existing
   `kenari:` section, so a file written before this ADR loads unchanged. Further providers go under `providers:` with
   the same connection settings (`base_url`, `sync_minutes`, `user_agent`, timeouts, `account_key_env`) and a `type`.
   The only type today is `kenari`, which names the wire protocol (OpenAI-shaped calls, kenari's catalog, plan and
   account endpoints); it may point at any host that speaks it. A new protocol means a new type, not a new code path in
   the router.
2. **A key belongs to one provider** (`provider:` on the key, empty = `kenari`). A key is only ever used for a model of
   its provider (`Key.Offers`, `Pool.KeysFor`), and `Check` refuses any other. Each provider has its own account, so
   rate limits, free quota, plan windows and cooldowns of one upstream never affect another.
3. **A model's id says where it lives.** The built-in provider's models keep their bare ids, so every pool, preset and
   log row written before keeps its meaning. A model of another provider is `<provider>/<id>`: that is what a pool
   lists, what the log records, and what `X-Xellter-Model` says. The name sent upstream is the bare one
   (`Model.WireID`, the same field a local model already used).
4. **The catalog is merged** (`catalog.Merge`). The first provider is the snapshot's base: its plans, free tier and
   exchange rate are the snapshot's. A secondary provider that cannot be reached keeps the models of its last good
   fetch, so one flaky upstream does not take its presets out of the ranking. If the built-in one fails, the refresh
   fails, as before.
5. **What stays kenari's.** Plans, the free tier, the top-up threshold, the plan strip on the Overview and the X search
   service are kenari concepts. They live under the `kenari` provider and are not generalised until a second provider
   has something of the same shape.
6. **The panel offers a provider choice only when there is more than one.** With a single provider nothing changes on
   screen except the wording, which no longer says "kenari" where it means "the upstream".

## What a client has to change

Nothing, for anything that works today: the `/v1` API, the preset names, the xellter keys, the usage endpoints and the
configuration file are unchanged. Two things are new capabilities, not migrations:

- **To use a model of another provider** the owner adds a key for that provider and ticks the model (listed as
  `<provider>/<model>`) in a preset. An app keeps asking for the preset's name; it never sees the prefix, except in
  the `X-Xellter-Model` response header and in `/v1/usage/models`, which now can name `<provider>/<model>`.
- **A rejection reason changed wording**: "not in the kenari catalog" is now "not in the catalog". Anything that matches
  that text (nothing in this repo does) must match the new one. The no-capacity errors now say "upstream key" instead of
  "kenari key".

Two things a client might notice and that are deliberately left alone: `/v1/models` still reports `owned_by: "kenari"`
for presets, and `/v1/usage/keys` still splits by the upstream key's name.

## Consequences

- A second provider is a configuration entry, a key and a model tick; no code.
- A free or discounted model of a second provider joins the pool of every preset of its category, but only for a client
  that holds a key there (ADR 0022 already required a key that may call it).
- Eviction, health, speed measurement and the effort refusals are keyed by the model id, so they follow the qualified id
  and stay separate per provider.
- Not done: a provider type other than `kenari`, per-provider plan windows on the Overview, and any change to the
  `/v1/usage/*` shapes.
