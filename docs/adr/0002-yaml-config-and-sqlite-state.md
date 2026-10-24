# 0002 — One YAML config as source of truth, SQLite only for runtime state

- Status: Accepted
- Date: 2026-10-24

## Context
Much of the behaviour should be configurable from a panel, but the config must also be readable and editable by humans and AI agents, versionable, and safe to publish as an example in a public repo.

## Decision
- Everything a person decides (upstreams, clients, shadow models, rules, weights, benchmarks) lives in one YAML file. The panel edits the same structure, validates before saving, keeps the previous 50 versions and can restore them. Changes hot-reload through `app.Apply`.
- Secrets never live in the config: upstream key values are in `secrets.env` (0600) referenced by name; client keys are stored as sha256 hashes.
- SQLite holds only runtime state: request log (metadata only), health stats, JEV scores, first-seen dates, admin sessions, async job routing. In-memory counters (rpm, quotas, plan ledger) are rebuilt from the log on start.

## Consequences
- The panel rewrites the YAML and loses comments; `config.example.yaml` carries the documentation.
- Backing up means copying `xellter.yaml`, `data/`.
