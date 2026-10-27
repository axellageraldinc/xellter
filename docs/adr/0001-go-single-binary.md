# 0001 — Write xellter from scratch in Go as one static binary

- Status: Accepted
- Date: 2026-10-24
- Deciders: maintainer

## Context
9router (Node, Next.js 16, React 19) covers the idea but is heavy: a framework runtime, many providers, OAuth flows, cloud sync, a tunnel and an MITM bridge. We need something very light and very fast for a personal server whose only upstream is kenari.id.

## Decision
Write a new project in Go, compiled with `CGO_ENABLED=0` into one binary. Dependencies: `modernc.org/sqlite` (pure Go), `gopkg.in/yaml.v3`, `golang.org/x/crypto/scrypt`. The admin UI is plain HTML/CSS/JS embedded with `go:embed`; no build step.

## Alternatives considered
- **Fork 9router** — brings Next.js/React and a feature set we would mostly delete.
- **Rust** — smaller and faster still, but slower to build and change; the proxy is I/O bound so Go's overhead is negligible.
- **Node without a framework** — simpler to write but larger idle memory and slower cold start.

## Consequences
- Idle memory and startup are small; deployment is copying one file.
- Streaming is a plain copy loop with flushes, so latency overhead is microseconds.
- We own every feature (translation between wire formats is not needed because kenari speaks both OpenAI and Anthropic wires).
