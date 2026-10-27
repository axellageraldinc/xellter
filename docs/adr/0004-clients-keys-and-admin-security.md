# 0004 — One key per client, admin behind host allowlist + password + TOTP

- Status: Accepted
- Date: 2026-10-24
- Deciders: maintainer

## Context
Several apps share one gateway; kenari only sees the upstream key, so only xellter can attribute usage and limit blast radius. The panel will later sit behind a public hostname, and the repo will be public.

## Decision
- Each client has exactly one xellter key (random, shown once, stored as sha256) with its own allowed presets, optional direct-model globs (off by default), upstream restriction and daily budget. A key cannot be used by another client.
- Admin login uses the same scheme and record format as the app it replaces: username + scrypt (N=2^17, r=8, p=1) + mandatory TOTP or single-use recovery code, rate limited (5 per 15 min per client, 30 global). Sessions are random tokens stored as hashes, cookie `HttpOnly` + `SameSite=Strict` (+ `Secure` behind HTTPS), writes need an `X-Xellter` header.
- `/admin` answers only for `Host` values in `server.admin_hosts`; any other hostname gets 404, so exposing the data plane publicly does not expose the panel.
- The request log stores metadata only.

## Consequences
- Password change / 2FA re-enrolment is a CLI operation for now (`xellter admin init` after removing the record).
- A shared kenari plan budget is modelled per account (`upstreams[].account`) because keys of one account share it.
