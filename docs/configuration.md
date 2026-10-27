# Configuration reference

One YAML file (`xellter -config`, default `xellter.yaml`, or `$XELLTER_CONFIG`). Missing fields take
the defaults below. The panel's Config tab edits the same file, validates it, keeps a history under
`<data_dir>/config-history` (last 50) and can restore a version. Run `xellter check` to validate
without starting.

Unknown keys are ignored — except the ones the rewrite deleted, which are **refused** (see the last
section).

## Shape

```yaml
server:   { listen, data_dir, secrets_file, admin_hosts, max_body_mb, log_retention_days,
            trusted_proxy, session_hours, auth_record }
kenari:   { base_url, sync_minutes, user_agent, first_byte_timeout_s, music_timeout_s, account_key_env }
routing:  { rank_refresh_minutes, top_models, evict_errors, evict_window_minutes,
            evict_cooldown_minutes, avoid_sunset_days, sticky_session }
clients[]:
  - { name, enabled, purpose, key_hash, key_hint, note,
      keys[]:    { name, key_env },
      presets{}: { <preset name>: { category, optimization, models[] } } }
```

## server

| key | default | meaning |
|---|---|---|
| `listen` | `127.0.0.1:8480` | bind address; must contain `:`. Keep it on loopback and put Tailscale/Caddy/cloudflared in front |
| `data_dir` | `data` | SQLite, catalog cache, secrets, config history. Relative paths resolve against the config file |
| `secrets_file` | `<data_dir>/secrets.env` | `NAME=value` lines, 0600; written by the panel |
| `auth_record` | `<data_dir>/auth-record.json` | admin login record (`xellter admin init`) |
| `admin_hosts` | `[localhost, 127.0.0.1]` | `Host` values allowed to reach the panel; globs allowed; anything else is 404. Add the tailnet name |
| `max_body_mb` | `64` | request body limit (413 above it) |
| `log_retention_days` | `30` | request-log retention; purged every 6 h |
| `trusted_proxy` | `true` | honour `X-Forwarded-For` (rate limiting, login) and `X-Forwarded-Proto` (Secure cookie) |
| `session_hours` | `12` | admin login lifetime |

## kenari

| key | default | meaning |
|---|---|---|
| `base_url` | `https://kenari.id` | upstream gateway |
| `sync_minutes` | `10` | catalog refresh interval |
| `user_agent` | `xellter/1.0` | sent upstream; kenari blocks the default Go UA |
| `first_byte_timeout_s` | `60` | per attempt; non-streaming requests get 3x |
| `music_timeout_s` | `900` | music generation timeout |
| `account_key_env` | `KENARI_ACCOUNT_KEY` | secret name of an optional full-access key. It only reads plan, usage and balance, never serves a request. Must look like `UPPER_SNAKE` |

## routing

The seven knobs that survived the rewrite. Everything a preset or a model decides is not here: the
optimization lives on the preset, and RTK, retry-once, key cooldown, the breaker and the reference
request are constants. The panel does not edit `routing`: change it in this file or through
`PUT /api/config`.

| key | default | meaning |
|---|---|---|
| `rank_refresh_minutes` | `5` | rebuild every ranking on this interval; also rebuilt after every catalog sync and every config change. Keep it >= 1: validation accepts `0` (and clamps a negative to it), but the refresh loop cannot start with a zero interval and the process panics at start-up |
| `top_models` | `10` | how many models a preset routes against; 1..50 |
| `evict_errors` | `3` | failures that take a model out of a preset |
| `evict_window_minutes` | `60` | the window those failures are counted in; >= 1 |
| `evict_cooldown_minutes` | `720` | how long the model stays out of that preset; >= 1 |
| `avoid_sunset_days` | `3` | a model retiring sooner than this is rejected when ranking |
| `sticky_session` | `true` | one model per session, for the life of the session (ADR 0020) |

There is no way to switch the timer off: `0` is not a working value (see above). Rankings are always
rebuilt after a catalog sync, after a config change and from the panel's Recalculate button.

## clients[]

A client is one app that calls xellter. It owns its kenari keys, one purpose and at most one preset
per category.

| key | meaning |
|---|---|
| `name` | `[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}`, unique |
| `enabled` | a disabled client authenticates as 403 |
| `purpose` | `general` \| `coding` \| `agentic` \| `vision` \| `bulk`; empty means `general`. Changes how the **text** preset is scored, never which models are in it (ADR 0018) |
| `key_hash` | sha256 hex of the xellter key the app uses; created by `xellter client add` or the panel. Never the key itself |
| `key_hint` | last four characters of that key, for display |
| `keys[]` | the client's kenari API keys, see below |
| `presets{}` | preset name -> preset, at most one per category, see below |
| `note` | free text |

### clients[].keys[]

A kenari key is a name and a secret name. xellter detects the account, the plan, the tier, the limits
and the models a key may call by itself.

| key | meaning |
|---|---|
| `name` | `[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}`, unique inside the client |
| `key_env` | name of the secret holding the value, `UPPER_SNAKE`; must be unique across the whole file. `xellter key add` derives it from client and key name |

The value lives in `data/secrets.env` (0600), never in the YAML.

### clients[].presets[]

Key = the model name the app sends. The name is the owner's own (ADR 0021): letters, digits, dot,
dash and underscore, unique inside the client. The `category` says what the preset serves, and a
client holds at most one preset per category, so a second preset of a kind is refused.

| Category | Suggested name | kenari endpoint |
|---|---|---|
| `chat` | `text` | `/v1/chat/completions`, `/v1/responses`, `/v1/messages` |
| `images` | `image` | `/v1/images/*` |
| `videos` | `video` | `/v1/videos` |
| `audio_speech` | `tts` | `/v1/audio/speech` |
| `audio_transcription` | `stt` | `/v1/audio/transcriptions` |
| `music` | `music` | `/v1/music` |
| `systemone` | `systemone` | `/v1/models?modality=systemone` |

| key | meaning |
|---|---|
| `category` | the kind of model the preset serves; required for a name that is not one of the suggested ones. A config written before ADR 0021 still loads: the old names keep their meaning |
| `optimization` | `cheapest` \| `balanced` \| `best_quality`; empty means `balanced` |
| `models` | the pool: explicit catalog model ids, no duplicates, no blanks. The optimization only orders what is listed here |

Every free model and every model whose promo is running is in the pool of every preset of that
category, whether it is listed here or not (ADR 0022). The added models are the ones the client's
own keys may call; they leave the pool on their own when the promo ends or kenari drops the model.
The panel shows them in their own list on the preset card.

A new preset starts with an empty pool and `balanced`. A model id that leaves kenari's catalog is
reported as `not in the kenari catalog` instead of disappearing from the list.

## Removed sections are refused

The rewrite deleted the ranking and routing machinery. A file that still carries any of it fails to
load with a message naming **ADR 0017** and pointing at `docs/rewrite-plan.md`, because unknown keys
are otherwise ignored and a retired knob would look like it still works.

| Refused | Was |
|---|---|
| `rules` | `free_bonus`, `plan_bonus`, `plan_cost_factor`, `promo_bonus_max`, `promo_min_remaining_hours`, `upcoming_lookahead_hours`, `avoid_sunset_days`, `sticky_minutes`, `stats_window_hours` |
| `resilience` | `max_attempts`, `cooldown_base_s`, `cooldown_max_s`, `breaker_failures`, `breaker_open_s` |
| `rtk` | `enabled`, `min_chars`, `max_chars`, the five filters, `diff_context` — RTK is always on |
| `benchmarks` | owner-typed 0..100 scores — quality is fetched |
| `presets.<name>.weights` | per-preset component weights |
| `presets.<name>.require` | `tool_call`, `vision`, `reasoning` gates |
| `presets.<name>.min_quality` | quality gate |
| `presets.<name>.max_candidates` | list size — `routing.top_models` replaces it |
| `presets.<name>.strategy` | `round_robin` / `priority` — routing is always round-robin |
| `presets.<name>.rr_band` | round-robin band |
| `presets.<name>.mode` | `auto` / `combo` — a preset is a pool plus an optimization |
| `presets.<name>.cost_profile` | reference request — fixed per category |
| `presets.<name>.description` | free text |

Client-level `passthrough`, `daily_budget_idr` and `seeded` are **not** refused: they are silently
ignored, so an old file still loads. Being in a preset's pool is the allow list now (ADR 0019), the
per-client budget is gone, and presets are never generated.

## Migrating an old file

```sh
xellter migrate -config xellter.yaml           # prints the new file and a list of manual follow-ups
xellter migrate -config xellter.yaml -write    # keeps the old one as xellter.yaml.pre-rewrite
```

The migration keeps server and kenari settings, every client with its keys, hash, hint and note, and
every preset whose name is one of the seven (name, category and model list are preserved). The
purpose becomes `general` and the optimization `balanced`. It reports every preset left with an empty
pool and every preset dropped because its name is not a fixed one. A preset whose stored `modality`
contradicts its fixed name fails validation, and the command prints that error instead of writing.
Secrets are untouched.

A pool is only carried over when the old preset listed explicit models (a `combo`); an `auto` preset
becomes an empty pool, so its models must be ticked in the panel before it serves. See "Known gaps" in
`docs/plan.md`.
