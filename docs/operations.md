# Operations

## First install
```bash
make build
install -d -m 700 data
./bin/xellter admin init -credentials-out data/FIRST-LOGIN.txt     # username, password, TOTP secret, recovery codes
./bin/xellter client add -name demo -out data/demo.key             # the xellter key the app will use
echo 'kn-...' | ./bin/xellter key add -client demo -name main      # a kenari key for that client
./bin/xellter serve
```
The same can be done in the panel (Clients, Add client, Add key). `admin init -from-record <file>` reuses an existing login record instead of generating one.

## systemd (user)
A user systemd unit `xellter.service` (not shipped in the repository) runs `bin/xellter serve -config xellter.yaml` from the project directory. After editing: `systemctl --user daemon-reload`. Logs: `journalctl --user -u xellter -f`.

A second pair, `xellter-backup.service` and `xellter-backup.timer`, takes a daily snapshot (see
"Backups" below). The timer is enabled and running; it survives reboots because it is
`Persistent=true`.

## Tailscale
```bash
tailscale serve --bg --https=8445 http://127.0.0.1:8480
```
The tailnet URL is `https://<host>.<tailnet>.ts.net:8445/`; add that host name to `server.admin_hosts` to open the panel. Remove with `tailscale serve --https=8445 off`.

## Public domain
The panel is the site: it is served at the root of its host and its API is at `/api`, so
`https://xellter.example.com` opens the panel and `https://xellter.example.com/v1` serves the
clients. Because the panel answers only for hostnames listed in `admin_hosts`, a public hostname
that is not in that list serves `/v1/*` and `/healthz` and returns 404 for everything else. Still
prefer Cloudflare Access in front of the public name.

The tunnel (`~/.cloudflared/xellter.yml`, `cloudflared-xellter.service`) points
`xellter.example.com` at `http://127.0.0.1:8480`, so no change was needed there.

## Day to day
- **Overview:** health banner with warnings you can act on, 24 h numbers, plan budget and balance, per-client summary.
- **Clients -> a client:** its kenari keys (add, replace, remove, settings) and its presets: the pool, the optimization, the sticky toggle, "Serving now" and why the order is what it is, and **Out for now** (the models taken out of the preset, why, and when they return). Spend is shown, never capped.
- **Add a client:** Clients, Add client. The xellter key is shown once. Then Add key on the client page; presets appear a few seconds after the key is checked.
- **Rotate a client key:** client page, New xellter key (the old key dies immediately).
- **Plan numbers:** Overview, Kenari plan & balance, Add account key (a full-access kenari key). Without it the plan budget is estimated.
- **Usage:** per client, preset, model or kenari key, with a request list; filter by client and period.
- **A client got `503`/`429`:** the Overview banner names the key or account that is paused and why; the client page has "Re-check".

## Restarts

State that survives a restart: the configuration, the request log, the learned leaderboard rows, the
**session pins** (so a restart does not move conversations, ADR 0028), the effort refusals, and the models
that are out with their health records (ADR 0029). State that does not: round-robin counters, key cooldowns and
breakers, and the token-estimate correction. A restart forgives every key, which is the safe direction.

## Backups

`xellter-backup.timer` (user systemd) takes a snapshot every day at 04:30 and keeps the last 14. It
writes to `~/apps/xellter/xellter-backups/`, **outside** `data/`, because `data/` is the thing that
gets lost. Each copy is the database, `secrets.env` and `xellter.yaml`, all mode 0600.

```bash
systemctl --user list-timers xellter-backup.timer   # when it ran and when it runs next
systemctl --user start xellter-backup.service         # take one now
./scripts/xellter-backup.py data xellter-backups 14   # the same, by hand
```

The database runs in WAL mode, so the script uses SQLite's **online backup API** rather than copying
the file: a plain `cp` of a database that is being written to can capture a torn page.

To restore, stop the service, put the database back and start it again:

```bash
systemctl --user stop xellter.service
cp xellter-backups/xellter-20261027-154624.db data/xellter.db
rm -f data/xellter.db-wal data/xellter.db-shm     # the WAL belongs to the old database
systemctl --user start xellter.service
```

**Never** copy `xellter.db` with `cp` while the service is running, and never delete `data/` while
the service is up: the request log, the leaderboard scores and the admin sessions all live there.
The clients and their presets are in `data/xellter.db` (ADR 0037); the key values and the login record are in `data/secrets.env` and
`data/auth-record.json`; the log itself is a history, and losing it costs the usage history, not
the service.

## If the history was lost

`data/xellter.db.marker` records that the database existed. When xellter starts and finds a
database with no requests in it while the marker says there was one, it says so in the log:

```
xellter: the request log in /home/wongjava/apps/xellter/data is empty but the database existed
since 2026-10-27T15:51:36Z: the history was lost (the database file was deleted or replaced)
```

The service still starts: a warning beats a gateway that refuses to come up. The message names the
time the database was created, so the loss is dated. Restore the newest copy from
`xellter-backups/` with the steps above.

## When a model fails or is slow

For every request xellter builds a list of attempts, one per usable (model, key), in ring order: the
session's pinned model first, then the preset's models in turn. It moves to the next model **before the
first byte reaches the client** when:

- the model or key answers 5xx, 408, 402 (no balance), 429 (rate or quota limit), 401/403 (key rejected),
  or "model not allowed / not found". A retriable failure retries the same model once first;
- the connection fails, or the response headers do not arrive within `kenari.first_byte_timeout_s`;
- a **streamed chat** answer sends headers but no first token within `routing.first_token_timeout_s`
  (15 s, plus 1 s per 10 000 prompt tokens). Some free upstreams queue for a minute before they say
  anything; this is what moves the caller on. The last model that can be tried is never timed out.

A genuine client mistake (400 for a bad body) is returned as is: another model would fail the same way.
Not covered: a stream that breaks **after** the first byte, and a model that answers 200 with a poor
answer.

**Taking a model out of a preset.** Per client and preset (ADR 0020, 0029). All of these keep the model
out of **that preset only**; the ring skips it and the next best model takes its place at once:

| Why | Rule | Out for |
|---|---|---|
| errors | `evict_errors` (3) failures in a row inside `evict_window_minutes` (60) | `evict_cooldown_minutes` (720) |
| unreliable | `fail_percent` (40) of its last `health_min_requests` (6) or more requests failed | `quarantine_minutes` (30), doubling |
| slow | median first token above `slow_first_token_s` (10) over the same requests | `quarantine_minutes` (30), doubling |

A model that comes back is judged afresh, on half as many requests. If it is bad again it goes out for
twice as long, up to `evict_cooldown_minutes`; a healthy judgement resets the doubling. A preset is
never emptied: the last two models stay in whatever their record. The models that are out and their records
are saved every minute and on shutdown, so a restart does not forgive them. Every time a model is taken out an
event is stored; the preset card shows them under "Taken out before".

**A conversation leaves a slow model.** If the model a session is pinned to waits longer than
`repin_first_token_s` (15) for two of the session's last three requests, the pin is dropped and the
model goes to the end of that session's order. The next model that answers becomes the pin.

### Runbook: replies are slow

1. Open the client's preset in the panel. **Out for now** shows who has already been taken out and why.
2. The **Last 24 h** column of the same card gives, per model, the median first-token wait, the share of
   failed requests and the request count. A model with a long wait that is not out is either under
   `health_min_requests` or under the limit; lower `slow_first_token_s` or `health_min_requests` for a stricter
   rule. **Taken out before** lists who went out earlier, and why. `GET /api/requests` has the raw rows.
3. Remove a model that is always slow from the preset's pool in the panel. The free models of the
   category are added to every pool on their own (ADR 0022) and cannot be removed from one preset; the
   health rules are what keep the slow ones out.
4. The reasoning effort a client asks for is forwarded as it is and does not choose a model (ADR 0028).
   Most models ignore `none`; a model that refuses the field is served without it. If a client is slow
   because a model thinks for thousands of tokens, the fix is the pool, not the effort.
5. `journalctl --user -u xellter -f` prints one line each time a model goes out
   (`client/model out of preset p: slow: median first token 12.3s over its last 7 requests (limit 10s)`)
   and each time a session leaves a model.

### Runbook: a conversation keeps changing model

Check that the client sends a session id (`X-Xellter-Session`, `X-Session-Id` or `prompt_cache_key`). Without
one xellter identifies the conversation by its first user message (ADR 0030); a client that rewrites that
message (for example after compressing its history) starts a new session. `GET /v1/session?session=<id>`
shows the live pin and every model the session has used. Also check that the preset is sticky.

## Is the request log complete?

`GET /healthz` reports `log_dropped` (rows thrown away because the write queue was full) and `log_lost` (rows the
database refused). Both should be `0`. If they are not, the usage page and the health rules are working from
fewer requests than were served; the log also says so once a minute. The usual cause is a slow or full disk.

## Security model
- Client keys: random 192-bit, stored as sha256, shown once; a key belongs to one client.
- Upstream key values: only in `data/secrets.env` (0600), never in config, logs or responses.
- Admin: Host allowlist, scrypt (N=2^17) + mandatory TOTP, single-use recovery codes, 5 attempts/15 min per client and 30 global, `HttpOnly` + `SameSite=Strict` cookie (`Secure` behind HTTPS), `X-Xellter` header required on writes, CSP and frame denial.
- Request log has metadata only (client, model, tokens, cost, timing, error), no prompts or answers.
- Reset a lost login or rotate the authenticator: `xellter admin init -force -credentials-out data/FIRST-LOGIN.txt`, then restart the service (it ends all sessions and prints a new password, TOTP secret and recovery codes).
