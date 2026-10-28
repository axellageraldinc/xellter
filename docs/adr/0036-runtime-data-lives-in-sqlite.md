# ADR 0036 — Runtime data lives in SQLite

Date: 2026-10-28

## Status

Accepted.

## Context

Besides the database, `data/` held four more files xellter itself reads and
writes at run time: `catalog.json` (the last good catalog), `login-state.json`
(login attempt counters), `recovery-used.json` (spent recovery codes) and
`config-history/` (the last 50 versions of the config).

## Decision

All four now live in `data/xellter.db`: the first three in the `kv` table, the
history in `config_history`. On start, `importLegacyFiles` copies the old files
in when the database holds nothing yet, and never touches the files, so they
can be deleted by the owner.

Stays in files, on purpose: `xellter.yaml` (the owner's config, edited by hand
and by the Config tab), `secrets.env` (secrets), and `auth-record.json` (the
admin credentials, written by `xellter admin init`).

A credential reset (`admin init -force`) deletes the counters and spent codes
from the database.
