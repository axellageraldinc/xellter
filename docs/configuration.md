# Configuration reference

One YAML file (`xellter -config`, default `xellter.yaml`, or `$XELLTER_CONFIG`). Missing fields take the defaults below; the panel (Config tab) edits the same data and keeps history. Unknown keys are ignored. Run `xellter check` to validate.

## server
| key | default | meaning |
|---|---|---|
| `listen` | `127.0.0.1:8480` | bind address; keep it on loopback and put Tailscale/Caddy/cloudflared in front |
| `data_dir` | `data` | relative paths resolve against the config file |
| `secrets_file` | `<data_dir>/secrets.env` | `NAME=value` lines (0600); written by the panel's "Set key value" |
| `auth_record` | `<data_dir>/auth-record.json` | admin login (`xellter admin init`) |
| `admin_hosts` | `[localhost, 127.0.0.1]` | `Host` header values that may open `/admin`; others get 404. Add the tailnet name. Globs allowed |
| `trusted_proxy` | `true` | use `X-Forwarded-For` for rate limiting and `X-Forwarded-Proto` for the Secure cookie |
| `max_body_mb` | `64` | request body limit |
| `log_retention_days` | `30` | request log retention |
| `session_hours` | `12` | admin session lifetime |

## kenari
`base_url` (`https://kenari.id`), `sync_minutes` (10), `first_byte_timeout_s` (120; non-streaming requests get 3×), `music_timeout_s` (900), `user_agent`.

## upstreams[]
`name`, `key_env` (secret name, e.g. `KENARI_KEY_BABU`), `enabled`, `tier` (`free`|`topup`|`plan`), `plan` (kenari plan name for `tier: plan`), `account` (keys of one kenari account share plan budget), `rpm` / `daily_free` (override tier limits; `-1` = unlimited), `allow` (model globs; usually learned by probing), `note`.

## clients[]
`name`, `enabled`, `key_hash` (sha256 of the client key; never the key), `key_hint`, `shadows` (`["*"]` = all), `passthrough` (real-model globs the client may call directly; empty = none), `upstreams` (restrict to named upstreams), `daily_budget_idr` (catalog cost; 0 = unlimited), `rtk` (`null` = follow global), `note`.

## shadows{}
Key = the model name clients send.

| key | meaning |
|---|---|
| `modality` | `chat`, `images`, `videos`, `audio_speech`, `audio_transcription`, `music` |
| `mode` | `auto` (rank the catalog) or `combo` (use `models` in order) |
| `models` | combo list |
| `allow` / `deny` | model globs (auto) |
| `require` | `tool_call`, `vision`, `reasoning`, `min_context` |
| `min_quality` | 0..1 quality gate |
| `max_candidates` | size of the main list |
| `strategy` / `rr_band` | `round_robin` rotates within `rr_band` of the top score; `priority` always starts with the best |
| `weights` | relative weights of `cost`, `benchmark`, `jev`, `reliability`, `context` |
| `cost_profile` | reference request: `input_tokens`, `output_tokens`, `cached_ratio` (chat); `images`; `seconds`, `variant` (video/STT); `chars` (TTS) |

Defaults: `xl-chat` (30/25/20/15/10), `xl-fast`, `xl-code`, `xl-image`, `xl-video`, `xl-tts`, `xl-stt`, `xl-music`.

## rules
`free_bonus` .10, `plan_bonus` .08, `plan_cost_factor` .3 (a plan-covered request counts for this share of its list price, because plan budget is finite), `promo_bonus_max` .05, `promo_min_remaining_hours` 12, `upcoming_lookahead_hours` 24, `new_model_days` 14, `canary_percent` 5, `canary_min_requests` 20, `canary_min_success` .9, `avoid_sunset_days` 3, `sticky_minutes` 30, `stats_window_hours` 24.

Sticky sessions are keyed by `X-Xellter-Session` (also `X-Session-Id`, `X-Claude-Code-Session-Id`), `prompt_cache_key`, `user`, or a hash of the first messages. Identical first messages therefore stay on one model.

## resilience
`max_attempts` 4, `cooldown_base_s` 30, `cooldown_max_s` 900, `breaker_failures` 5, `breaker_open_s` 120.

## jev
`enabled` (false), `model` (`jev-1-13:free`), `upstream` (restrict calls to one key), `daily_budget_idr` (1000), `every_hours` (24), `pass_threshold` (.6), `prompts` (golden prompts). Candidates answer the prompts, the judge answers one `noul` question per answer ("is this correct, complete, on-topic, right language?"); the average is stored per model.

## rtk
`enabled`, `min_chars` (2000), `max_chars` (12000), `git_diff`, `grep`, `ls`, `logs`, `truncate`, `diff_context` (1). Per request: header `X-Xellter-RTK: off`.

## benchmarks{}
`model-id: 0..100`. Replaces the price proxy for that model.
