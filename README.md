# xellter

A small LLM gateway in front of [kenari.id](https://kenari.id). An app calls an OpenAI-compatible
endpoint with its own xellter key and asks for a **preset name** the owner chose (`chat`, `image`, …);
xellter picks a real model from that preset's pool, keeps one model per conversation, retries a
failure once, and takes a model out of that preset after three errors. One static Go binary, SQLite,
no runtime dependencies.

## The three optimizations

A preset is a **pool of models** plus **one optimization**, and the preset has a name of your own.
Ranking only orders the pool; it never removes a model for being cheap, expensive or unknown. Every
free model and every model whose promo is running is in the pool of every preset of that category,
whether you tick it or not, and leaves it again when the promo ends.

| Optimization | Order | The list |
|---|---|---|
| Cheapest | reference cost ascending, nothing else | half free + half plan-covered (5 + 5 at the default `top_models: 10`), each half by its own cost |
| Balanced | 50 % cost score, 50 % benchmark | top 10 (the default) |
| Best Quality | benchmark of the category only, floor at the pool median | top 10, models with no evidence excluded (a free or discounted model is never excluded for that) |

Cost is one number: the price of one reference request (for chat, 4 000 input tokens of which 70 %
are cache reads, plus 600 output tokens), promos included. Text quality comes from public
leaderboards fetched by xellter — Arena, SWE-bench Verified, Aider polyglot — never typed by the
owner. A client's purpose (General, Coding, Agentic, Vision & documents, Bulk) chooses the text
sources and never changes pool membership. Media presets (image, video, TTS, STT, music, System One)
have no public score wired into the ranking yet, so they order by cost and Best Quality serves
nothing there.

## Also

- **Routing.** Round-robin over the preset's top 10, session pinning (one model per conversation),
  retry once on a retriable error, then the next model, and three errors in an hour evict a model
  from that preset for 12 hours. Rankings rebuild every 5 minutes, after each catalog sync, and from
  the panel's Recalculate button.
- **RTK.** Tool output (`git diff`, `grep`, `ls`, logs, oversized text) is compressed before it is
  sent. Always on, no switch.
- **Plan numbers.** With an optional full-access account key, plan windows and balance come straight
  from kenari and sit next to xellter's own accounting.
- **Panel.** Overview, Clients, Usage, Config file. Login: username + password (scrypt) + TOTP.

## Run

```bash
make build                                     # -> bin/xellter (needs Go 1.27+)
install -d -m 700 data
./bin/xellter admin init -credentials-out data/FIRST-LOGIN.txt
./bin/xellter client add -name babu -out data/babu.key
echo 'kn-...' | ./bin/xellter key add -client babu -name main
./bin/xellter serve                            # http://127.0.0.1:8480/
```

Then open the panel, create the presets this client needs (one per category) and tick their models.

Upgrading an old config:

```bash
./bin/xellter migrate -config xellter.yaml -write   # old file kept as xellter.yaml.pre-rewrite
```

```bash
curl http://127.0.0.1:8480/v1/chat/completions \
  -H "Authorization: Bearer $(cat data/babu.key)" -H 'Content-Type: application/json' \
  -d '{"model":"text","messages":[{"role":"user","content":"hello"}]}'
```

`X-Xellter-Model` names the real model that answered; `X-Xellter-Attempt` the attempt number.

## Endpoints

| Method and path | Answers |
|---|---|
| `POST /v1/chat/completions`, `/v1/responses`, `/v1/messages` | chat, passed through unchanged (OpenAI chat, OpenAI Responses, Anthropic messages) |
| `POST /v1/systemone` | System One, passed through as-is to a `systemone` preset |
| `POST /v1/x/search` | the X search, passed through as-is to the key the client keeps for that service |
| `POST /v1/images/*`, `/v1/audio/speech`, `/v1/audio/transcriptions`, `/v1/videos`, `/v1/music` | media |
| `GET /v1/videos/{id}` | a video job, through the key that created it |
| `GET /v1/models`, `/v1/models/{id}` | the client's presets and the real models in its pools, with their prices |
| `GET /v1/session` | the model this session is pinned to, and what it has cost |
| `GET /v1/usage?range=24h\|30d` | traffic per model and billing kind (only successful requests count) |
| `GET /v1/usage/keys`, `/v1/usage/models` | traffic per kenari key name, per preset and model |
| `GET /v1/account/balance`, `/v1/account/quota` | kenari's balance and plan windows |
| `GET /healthz` | catalog size, age and degraded flag |

Anything else is refused with **404** and never forwarded: a request for an endpoint xellter does
not have would otherwise reach kenari as a model call, and its failure would be counted against
every model of the preset (ADR 0024).

## Integrate

Any OpenAI-compatible client works. Point it at the gateway, give it the client key, and set the
model to a preset name:

```bash
curl https://xellter.example.com/v1/chat/completions \
  -H "Authorization: Bearer $XELLTER_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"chat","messages":[{"role":"user","content":"hello"}]}'
```

- **The model you ask for is the preset, not a kenari model.** List what a client may ask for with
  `GET /v1/models`; the entry is the preset, `data[].models` are the real models behind it.
- **`X-Xellter-Session` keeps one model for the conversation.** Send a stable id (a chat id, a
  thread id); without it the pin follows a hash of the body. `GET /v1/session?session=<id>` says
  what that session is using and what it has cost.
- **`X-Xellter-Model` on the response** names the real model that answered; `X-Xellter-Attempt` is
  the attempt number (1, or 2 after one retry).
- **Usage** (`GET /v1/usage?range=24h|30d`) is priced from the catalogue and split by billing kind
  (`plan`, `payg`, `free`), in the shape kenari's own usage endpoint uses.
- **Streaming** is copied through unbuffered; the prompt and the response are never logged or stored.

### Custom endpoints

Two endpoints have no analogue in an OpenAI client, and both are passed through untouched — xellter
rewrites no field in the body:

**`POST /v1/x/search`** — the X (Twitter) search, which kenari bills per call from the account
balance. It is served by a key marked `capability: search`, never by a model key: a capability key is
never probed, never joins a pool and never serves a model call, so a pay-as-you-go key is only spent
on the calls the client asked for. kenari calls a 503 "failed to return a usable answer, not charged,
retry", so xellter retries up to three times. A successful search is logged as one paid row
(`x/search`, Rp 100 at the time of writing); a failed one is not charged and does not appear in usage.

```bash
curl https://xellter.example.com/v1/x/search \
  -H "Authorization: Bearer $XELLTER_KEY" -H 'Content-Type: application/json' \
  -d '{"query":"latest Rust release"}'
```

**`POST /v1/systemone`** — System One models, served by a preset whose category is `systemone`. They
are fetched from their own catalogue endpoint and priced like any other model, and they never mix
into a chat pool: `/v1/systemone` is the only path that reaches them.

## Documentation

| File | Contents |
|---|---|
| [docs/architecture.md](docs/architecture.md) | components, request flow, ranking maths, routing, endpoints, panel |
| [docs/configuration.md](docs/configuration.md) | every config key, and what is refused |
| [docs/operations.md](docs/operations.md) | deploy, Tailscale, runbook, security model |
| [docs/plan.md](docs/plan.md) | phase status and what is deliberately not done |
| [docs/adr/](docs/adr/) | architecture decision records |
| [skills/xellter/SKILL.md](skills/xellter/SKILL.md) | how to operate xellter (for LLM agents) |
| [AGENTS.md](AGENTS.md) | rules for AI coding agents working on this repo |

## Develop

```bash
./scripts/ci.sh   # gofmt, vet, tests, JS syntax; no network, no paid calls
```

## License

MIT, see [LICENSE](LICENSE).
