# xellter

A small, fast LLM gateway in front of [kenari.id](https://kenari.id): one OpenAI/Anthropic-compatible
endpoint that picks the cheapest model that is good enough, fails over between models and keys, and
shrinks tool output before it is paid for. One static Go binary, no runtime dependencies.

> Status: v1 feature-complete (see [docs/plan.md](docs/plan.md)). Built for a personal server; the
> upstream is kenari, but the code is split so another upstream can be added.

## What it does

- **Shadow models.** Clients call `xl-chat`, `xl-code`, `xl-image`, ...; xellter chooses the real model.
  Rank is computed from live kenari prices, promos, plans and free tiers, a quality score
  (benchmarks, a System One "JEV" judge, context size) and measured reliability.
- **Auto rules.** Free models, plan-covered models and models on discount are preferred; new models get
  a small canary share until proven; non-text modalities (image, video, TTS, STT, music) are ordered
  by price, free first.
- **Routing.** Round-robin or priority across models and keys, combos with fallback, session
  stickiness (keeps provider caches warm), cooldowns and a circuit breaker per key.
- **Many keys, many clients.** Several kenari keys behind one endpoint (restricted keys are probed
  for free); one xellter key per client with its own allowed models and daily budget.
- **RTK.** Compresses large `git diff`, `grep`, `ls` and log output in tool results before sending.
- **Admin panel.** Dashboard, live key quotas, ranking explanations, config editor with history and
  rollback. Login: username + password (scrypt) + TOTP, same record format as aixell.

## Quick start

```bash
make build                                   # -> bin/xellter (needs Go 1.27+)
cp config.example.yaml xellter.yaml          # edit upstreams
echo 'KENARI_KEY_BABU=kn-...' > data/secrets.env && chmod 600 data/secrets.env
./bin/xellter admin init -credentials-out data/FIRST-LOGIN.txt
./bin/xellter client add -name babu -out data/babu.key
./bin/xellter serve -config xellter.yaml     # http://127.0.0.1:8480/admin/
```

Point a client at it:

```bash
curl http://127.0.0.1:8480/v1/chat/completions \
  -H "Authorization: Bearer $(cat data/babu.key)" -H 'Content-Type: application/json' \
  -d '{"model":"xl-chat","messages":[{"role":"user","content":"hello"}]}'
```

The response header `X-Xellter-Model` tells which real model answered.

## Supported endpoints

`POST /v1/chat/completions`, `/v1/responses`, `/v1/messages` (Anthropic wire), `/v1/images/*`,
`/v1/audio/speech`, `/v1/audio/transcriptions` (multipart), `/v1/videos/*` (async jobs follow their key),
`/v1/music/generations`; `GET /v1/models` (lists the client's shadows), `GET /healthz`.

## Documentation

| File | Contents |
|---|---|
| [docs/architecture.md](docs/architecture.md) | components, request flow, ranking maths |
| [docs/configuration.md](docs/configuration.md) | every config option |
| [docs/operations.md](docs/operations.md) | deploy, Tailscale, runbook, security model |
| [docs/plan.md](docs/plan.md) | phases and deliverables |
| [docs/adr/](docs/adr/) | architecture decision records |
| [AGENTS.md](AGENTS.md) | rules for AI coding agents working on this repo |

## Develop

```bash
make test        # gofmt, vet, unit + integration tests (kenari is faked; no network, no paid calls)
```

## License

MIT, see [LICENSE](LICENSE).
