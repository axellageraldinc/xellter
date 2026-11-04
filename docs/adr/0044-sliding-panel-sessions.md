# ADR 0044 — Panel sessions slide, with a hard cap

- Status: Accepted
- Date: 2026-11-04
- Deciders: maintainer
- Amends: the fixed `session_hours` login lifetime of the first panel

## Context

A panel login was fixed at 12 hours from sign-in. It ended in the middle of use, and the panel did not notice: it kept
showing pages whose calls all failed with "login required" until the tab was reloaded. aixell, which this login was
ported from, keeps a user signed in while they use it, with an absolute limit. The goal is a panel that does not ask
for a password every day and is still as hard to attack as practical: 2FA is mandatory, so the cookie is the only
thing worth stealing, and its life should be as short as the convenience allows.

## Decision

1. **A session slides.** Each use pushes its expiry out to `server.session_days` (default 7) from now. The write is
   throttled to once an hour so a polling panel does not write on every request.
2. **A session has a hard cap.** `server.session_max_days` (default 30) after sign-in the session ends however often it
   was used. The cap is never shorter than the window.
3. **The cookie follows the window.** When the window moves, the response re-issues the cookie with the new `Max-Age`;
   without that the browser drops it at the old expiry while the server still accepts it.
4. **`server.session_hours` is retired** and refused on load, naming its replacement.
5. **A TOTP code works once.** The newest accepted time step is kept with the login counters, so it survives a restart;
   that step or any older one is refused (RFC 6238, 5.2). A credential reset clears it with the rest of the state.
6. **Only the proxy's address is believed.** With `trusted_proxy` the client address is the LAST `X-Forwarded-For`
   entry, the one the proxy in front appended, and only if it is an IP. Earlier entries are chosen by the client, so
   reading the first let anyone mint a fresh login rate-limit bucket per attempt.
7. **The panel reacts to a 401.** Any API call other than `login` and `me` that is answered 401 sends the panel back
   to the sign-in page.

## Consequences

- A stolen session cookie now lives up to 30 days instead of 12 hours if it keeps being used; an unused one still
  ends in 7. `logout-all` ends every session at once. The cookie stays `HttpOnly` and `SameSite=Strict`.
- Existing sessions get the new rules on their next use: a row with no refresh time counts from its creation.
- The `sessions` table gains a `last_refresh` column, added on open.
