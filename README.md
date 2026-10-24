# xellter

A small, fast LLM gateway in front of [kenari.id](https://kenari.id): one OpenAI/Anthropic-compatible
endpoint that picks the cheapest model that is good enough, fails over between models and keys, and
shrinks tool output before it is paid for. One static Go binary, no runtime dependencies.

> Status: v1 feature-complete (see [docs/plan.md](docs/plan.md)). Built for a personal server; the
> upstream is kenari, but the code is split so another upstream can be added.

## What it does

- **Clients, keys, presets.** You add a *client* (an app such as Babu or aixell), give it one or more kenari API keys, and xellter generates its **presets** from the models those keys can call, duplicates merged: `text`, `image`, `video`, `tts`, `stt` (and `music`). The app uses a preset name as the model; for every request xellter picks the best model behind it right now.
- **Auto rules.** Free models, plan-covered models and discounted models are preferred; new models get a small canary share until proven; image/video/speech presets are ordered by price, free first. Quality is gated by benchmarks you provide, a System One "JEV" judge and measured reliability.
- **Routing.** Round-robin or priority across models and keys, session stickiness (keeps provider caches warm), cooldowns and a circuit breaker per key, limits tracked per kenari account.
- **Real plan numbers.** With an optional full-access account key, plan budgets, reset times and balance come straight from kenari.
- **RTK.** Compresses large `git diff`, `grep`, `ls` and log output in tool results before sending.
- **Admin panel.** Clients and their keys, preset controls ("optimise for cheapest / balanced / best quality"), usage per client, preset and model, config editor with history. Login: username + password (scrypt) + TOTP.

## Quick start

```bash
make build                                   # -> bin/xellter (needs Go 1.27+)
install -d -m 700 data
./bin/xellter admin init -credentials-out data/FIRST-LOGIN.txt   # username, password, TOTP secret, recovery codes
./bin/xellter client add -name babu -out data/babu.key           # the xellter key your app will use
echo 'kn-...' | ./bin/xellter key add -client babu -name main     # a kenari key for that client
./bin/xellter serve                          # http://127.0.0.1:8480/admin/
```
Or do the same in the panel: Clients, Add client, Add key. Presets appear a few seconds after the key is checked.

Point a client at it:

```bash
curl http://127.0.0.1:8480/v1/chat/completions \
  -H "Authorization: Bearer $(cat data/babu.key)" -H 'Content-Type: application/json' \
  -d '{"model":"text","messages":[{"role":"user","content":"hello"}]}'
```

The response header `X-Xellter-Model` tells which real model answered.

## Supported endpoints

`POST /v1/chat/completions`, `/v1/responses`, `/v1/messages` (Anthropic wire), `/v1/images/*`,
`/v1/audio/speech`, `/v1/audio/transcriptions` (multipart), `/v1/videos/*` (async jobs follow their key),
`/v1/music/generations`; `GET /v1/models` (lists the client's presets), `GET /healthz`.

## Documentation

| File | Contents |
|---|---|
| [docs/architecture.md](docs/architecture.md) | components, request flow, ranking maths |
| [docs/configuration.md](docs/configuration.md) | every config option |
| [docs/operations.md](docs/operations.md) | deploy, Tailscale, runbook, security model |
| [docs/plan.md](docs/plan.md) | phases and deliverables |
| [docs/adr/](docs/adr/) | architecture decision records |
| [skills/xellter/SKILL.md](skills/xellter/SKILL.md) | how to change xellter (for LLM agents) |
| [AGENTS.md](AGENTS.md) | rules for AI coding agents working on this repo |

## Develop

```bash
make test        # gofmt, vet, unit + integration tests (kenari is faked; no network, no paid calls)
```

## License

MIT, see [LICENSE](LICENSE).
