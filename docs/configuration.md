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
`base_url` (`https://kenari.id`), `account_key_env` (secret name of the optional full-access account key; it only reads plan, usage and balance), `sync_minutes` (10), `first_byte_timeout_s` (60; non-streaming requests get 3×), `music_timeout_s` (900), `user_agent`.

## clients[]
A client is one app that calls xellter. It has its own kenari keys and its own presets.

| key | meaning |
|---|---|
| `name` | letters, digits, dot, dash, underscore |
| `enabled` | a disabled client gets 403 |
| `key_hash`, `key_hint` | sha256 of the xellter key the app uses, and its last 4 characters; never the key itself |
| `keys[]` | the client's kenari API keys, see below |
| `presets{}` | name -> preset. **Generated automatically** from the models the client's keys can call (see below); every field is editable |
| `seeded[]` | kinds (`chat`, `images`, `videos`, `audio_speech`, `audio_transcription`, `music`) that already got a generated preset, so a deleted or edited preset is never recreated |
| `passthrough` | real-model globs the app may also call directly; normally empty |
| `daily_budget_idr` | spending cap at list price, free and plan traffic included; 0 = unlimited |
| `rtk` | `null` = follow the global token saver setting |
| `note` | free text |

The panel shows none of the client-level knobs: they keep their defaults, and the defaults are the sensible ones (see ADR 0012).

### keys[]
A kenari key has a name and nothing else: `name` and `key_env` (the name of the secret that holds the key value in `data/secrets.env`, unique across clients). xellter detects the account, the plan, the tier and the limits by itself; the models a key may call are learned from kenari (a probe or a 400 answer), never configured. Rename a key in the panel; the secret moves with it.

### Generated presets
For each kind that has at least one model callable by the client's keys, xellter creates one preset once: `text` (chat), `image`, `video`, `tts` (text to speech), `stt` (speech to text), `music`. A client is only seeded after all its enabled keys have been probed. Preset names are predefined and a client has each at most once: the admin API refuses any other name or a kind that does not match its name. A preset can be edited or deleted (it is not recreated) and added back from the panel ("Add preset" lists only the missing ones). Preset names take precedence over real model names.

## presets (per client)
Key = the name the app sends as `model`.

| key | meaning |
|---|---|
| `modality` | `chat`, `images`, `videos`, `audio_speech`, `audio_transcription`, `music` |
| `mode` | `auto` (rank the callable models) or `combo` (use `models` in order) |
| `models` | combo list |
| `require` | `tool_call`, `vision`, `reasoning` |
| `min_quality` | 0..1 quality gate |
| `max_candidates` | size of the main list |
| `strategy` / `rr_band` | `round_robin` rotates within `rr_band` of the top score; `priority` always starts with the best |
| `weights` | relative weights of `cost`, `benchmark`, `jev`, `reliability`, `context` |
| `cost_profile` | reference request: `input_tokens`, `output_tokens`, `cached_ratio` (chat); `images`; `seconds`, `variant` (video/STT); `chars` (TTS) |

A preset has no model allow or deny list and no context-size gate: the models the client's keys may call are the ones it can use, and the context size is only a soft preference in the score (ADR 0012).

In the panel, a preset card offers the objective buttons ("Optimise for"): Cheapest (60/10/10/15/5, .35), Balanced (25/35/20/15/5, .50), Best quality (2/55/25/15/3, .64), the tie-break strategy, and the tool-calling and image-input requirements. Everything else keeps the backend default.

## rules
`free_bonus` .10, `plan_bonus` .08, `plan_cost_factor` .3 (a plan-covered request counts for this share of its list price, because plan budget is finite), `promo_bonus_max` .05, `promo_min_remaining_hours` 12, `upcoming_lookahead_hours` 24, `new_model_days` 14, `canary_percent` 5, `canary_min_requests` 20, `canary_min_success` .9, `avoid_sunset_days` 3, `sticky_minutes` 30, `stats_window_hours` 24.

Sticky sessions are keyed by `X-Xellter-Session` (also `X-Session-Id`, `X-Claude-Code-Session-Id`), `prompt_cache_key`, `user`, or a hash of the first messages. Identical first messages therefore stay on one model.

## resilience
`max_attempts` 4, `cooldown_base_s` 30, `cooldown_max_s` 900, `breaker_failures` 5, `breaker_open_s` 120.

## jev
`enabled` (false), `judges` (`[jev-1-13:free]`; an ordered list of System One models, reorder it in the panel), `daily_budget_idr` (1000), `every_hours` (24), `pass_threshold` (.6), `prompts` (golden prompts). Judging uses the dashboard's account key, never a client's key. Candidates answer the prompts, the judge answers one `noul` question per answer ("is this correct, complete, on-topic, right language?"); the average is stored per model.

The first judge in `judges` scores every answer. When kenari cannot serve it (5xx or a timeout) the next one in the list takes over, and after the configured ones any other free System One judge is appended automatically, so a single outage never stops a round. The old single-judge key `model` is still read and moved to the front of `judges` on load; it is never written back.

## rtk
`enabled`, `min_chars` (2000), `max_chars` (12000), `git_diff`, `grep`, `ls`, `logs`, `truncate`, `diff_context` (1). Per request: header `X-Xellter-RTK: off`.

## benchmarks{}
Your own quality scores, 0..100, per model id. They override the leaderboard. Without one, xellter uses the Arena leaderboard (`internal/bench`, refreshed daily from the public Hugging Face dataset) and, for models it does not list, an estimate from price.
`model-id: 0..100`. Replaces the price proxy for that model.
