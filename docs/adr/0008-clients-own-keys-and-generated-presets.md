# 0008 — Clients own their kenari keys; presets are generated per client

- Status: Accepted (supersedes the global `upstreams` + `presets` layout of the first build)
- Date: 2026-10-24
- Deciders: maintainer

## Context
The first build had a global pool of kenari keys, a global set of hand-configured presets, and clients that picked from both. It was hard to use: the owner thinks in terms of apps, each with its own key(s), and wants to see what each app can do and what it costs, not to configure routing tables.

## Decision
- The unit is the **client**. A client has its xellter key (hash only), one or more **kenari keys**, **presets**, an optional spending limit and a passthrough list. A client only ever uses its own keys.
- **Presets are generated.** After a client's keys are probed (free: kenari answers an unknown model with the key's allow list), xellter creates one preset per kind that has at least one callable model (`text`, `image`, `video`, `tts`, `stt`, `music`), once. Models are deduplicated naturally: a model reachable by several keys is one candidate with several keys. The `seeded` list keeps deleted or edited presets from being recreated or overwritten. Extra presets can be added by hand.
- Rankings are per `(client, preset)`; the quality, plan, free-tier and promo rules are unchanged (ADR 0003, 0006).
- An optional **account key** (`kenari.account_key_env`, full access, never used for requests) supplies real plan numbers and balance.
- Preset names have no prefix and are plain kinds (`text`, `image`...); they take precedence over real model names.
- The panel follows the same shape: Overview, Clients (-> client page with keys and presets), Usage, Settings.

## Consequences
- Breaking config change before any public release; the live config was migrated with a one-off script.
- Two clients using the same kenari account still share account-level limits (`account`, default `main`).
- A preset's "models available" is the number of distinct models that pass its requirements for that client.
