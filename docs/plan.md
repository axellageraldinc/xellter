# Plan and phases

Status: **P0-P6 delivered in the first build**; rollout and tuning continue (see "Next").

| Phase | Deliverable | Acceptance | Status |
|---|---|---|---|
| P0 Foundation | repo, docs, ADRs, CI (GitHub-hosted, pinned actions), Go skeleton | `scripts/ci.sh` green; secret scan clean | done |
| P1 Proxy core | OpenAI + Anthropic wire, streaming pass-through, per-client keys, usage accounting, SQLite log, `/healthz` | integration tests with a fake kenari | done |
| P2 Routing | preset models, combos, round-robin/priority, cooldown + breaker, session stickiness, catalog sync | failover tests: 5xx, 429, restricted key | done |
| P3 Auto-rank | free / plan / promo rules, tier quotas, plan ledger, quality gate and score, media combos | ranking fixtures; deterministic-score test | done |
| P4 Dashboard + login | panel for everything configurable, scrypt + TOTP login, history + rollback | login/CSRF/host/config tests; desktop + mobile check | done |
| P5 RTK | diff / grep / ls / log / truncate filters, per-client and per-request switch | unit tests; end-to-end size assertion | done |
| P7 Client-centric model | clients own kenari keys; presets generated per client from the models its keys can call; account key for real plan numbers; panel rebuilt on Tabler | tests for generation, per-client routing, account quota; browser check desktop + mobile | done |
| P6 Hardening + rollout | systemd unit, Tailscale exposure, REGISTRY, secret scan, clients moved one at a time | see "Rollout" | in progress |

## Rollout
1. Run on the tailnet only; owner tests with curl and the panel. (current)
2. Move **polifin** first (smallest key, one model), then **aixell**, then **Babu**: change `base_url` and key in each profile, keep the old direct key as a rollback.
3. Watch Overview and Usage for a few days; adjust weights and benchmarks.
4. Open the public domain (`xellter.<your-domain>`) and flip the repo to public when the owner says so.

## Next (not in v1)
- Benchmarks file curated for the owner's workloads.
- Weekly digest to Babu (discounts ending, new models, sunsets).
- Panel: change password / re-enrol 2FA (today: CLI), request playground.
- Prometheus `/metrics`.
- A second upstream provider (the catalog/route/proxy seams are provider-shaped but only kenari is implemented).

## Performance targets (measured in P6)
Binary < 20 MB (12.7 MB now), idle RSS < 30 MB, added latency p99 < 3 ms, streaming without buffering.
