# 0006 — Limits are per kenari account; plan data comes from the API; "preset" naming

- Status: Accepted
- Date: 2026-10-24
- Deciders: owner (Axell)

## Context
1. The first build tracked rpm, free quota and plan spend per key and hard-coded the plan name in config. kenari documents these limits as **per account** (free quota resets 00:00 UTC), and exposes the real plan name, budgets, usage, reset times and balance at `GET /v1/account/quota` and `/v1/account/balance` for *unrestricted* keys only. The hard-coded plan was wrong for this account (guessed Indie, actual Kreator).
2. "Shadow model" was a working title; the owner wants a clearer term.

## Decision
- Keys are grouped by `upstreams[].account`; rpm, free quota, plan budget, balance flag and cooldowns for 429 `rate_limit_exceeded` live on the account. Free quota counts since 00:00 UTC; error codes (`free_quota_daily`, `free_quota_rpm`, `plan_limit_reached`) map to account effects.
- xellter reads quota and balance with every key at start-up and every 5 minutes; the first key that is allowed becomes the account's quota key. Its numbers replace the configured plan and our estimates (our own spend since the last read is added). The configured `plan` is a fallback only.
- The concept is called **preset model** (config key `presets`, panel tab "Preset models"). Old `shadows` keys are migrated on load and the SQLite column is renamed. Default presets are named by purpose: `xl-balanced`, `xl-budget`, `xl-smart`, `xl-coding`, `xl-image`, `xl-video`, `xl-speech`, `xl-transcribe`, `xl-music`.

## Consequences
- With only restricted keys the plan budget stays an estimate; the panel says so and suggests adding an unrestricted key.
- kenari's usage endpoints share 60 requests/min per account; xellter uses two calls per account per 5 minutes.
