# ADR 0037 — Clients live in SQLite, the config file holds only internal settings

Date: 2026-10-28

## Status

Accepted. Narrows ADR 0036, which kept `xellter.yaml` whole.

## Context

`xellter.yaml` held two different things: internal settings (`server`, `kenari`,
`routing`, `local_models`), which are config, and the clients with their kenari
keys, presets and model pools, which the owner edits in the panel and which grow
with use. The second kind is data, not configuration.

## Decision

- The clients are stored as one JSON document under the `clients` key of the
  `kv` table in `data/xellter.db` (`Store.Clients`, `Store.SetClients`).
- `xellter.yaml` and the Config tab hold only the internal settings. Saving from
  the Config tab keeps the current clients; clients are changed on their own
  pages or with `xellter client add` / `key add`.
- `config.Save` writes the clients to the store and strips them from the file
  when it is given a store; without one (tests, `migrate` preview) they stay in
  the file. `config.AttachClients` loads them at start, and moves the clients
  an older file still carries into the database once, rewriting the file.
- Config history entries still restore everything: each one is the previous
  file with the previous clients appended.
- The in-memory `config.Config` is unchanged, so routing, proxy and panel code
  did not change.

## Consequences

The database is now the only copy of the clients. The daily `xellter-backup`
snapshot already covers it. Hand-editing clients in `xellter.yaml` no longer
works once the move has happened.
