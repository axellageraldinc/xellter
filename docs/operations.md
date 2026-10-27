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
A user systemd unit `xellter.service` runs `bin/xellter serve -config xellter.yaml` from the project directory. After editing: `systemctl --user daemon-reload`. Logs: `journalctl --user -u xellter -f`.

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
- **Clients -> a client:** its kenari keys (add, replace, remove, settings), its presets (what they serve now, "Optimise for", requirements; "Why this order?" explains the ranking), its spending limit.
- **Add a client:** Clients, Add client. The xellter key is shown once. Then Add key on the client page; presets appear a few seconds after the key is checked.
- **Rotate a client key:** client page, New xellter key (the old key dies immediately).
- **Plan numbers:** Overview, Kenari plan & balance, Add account key (a full-access kenari key). Without it the plan budget is estimated.
- **Usage:** per client, preset, model or kenari key, with a request list; filter by client and period.
- **A client got `503`/`429`:** the Overview banner names the key or account that is paused and why; the client page has "Re-check".

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
The clients, the keys and the login record are in `xellter.yaml`, `data/secrets.env` and
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

## When a model fails
The ranking is also the fallback order. For every request xellter builds up to `resilience.max_attempts` (4) attempts, best first, at most two keys per model, and moves to the next one **before the first byte reaches the client** when:
- the model or key answers 5xx, 408, 402 (no balance), 429 (rate or quota limit), 401/403 (key rejected), or "model not allowed/not found";
- the connection fails or nothing arrives within `kenari.first_byte_timeout_s` (streaming; non-streaming gets 3x).
The failed model/key is paused with growing back-off (and a circuit breaker after repeated errors), so the next request does not try it again at once. A genuine client mistake (400 for a bad body) is returned as is: another model would fail the same way. Not covered: a stream that breaks **after** the first byte (the client has already received part of the answer), and a model that answers 200 with a poor answer. Lower `first_byte_timeout_s` if you prefer faster fallback over patience with slow reasoning models.

## Security model
- Client keys: random 192-bit, stored as sha256, shown once; a key belongs to one client.
- Upstream key values: only in `data/secrets.env` (0600), never in config, logs or responses.
- Admin: Host allowlist, scrypt (N=2^17) + mandatory TOTP, single-use recovery codes, 5 attempts/15 min per client and 30 global, `HttpOnly` + `SameSite=Strict` cookie (`Secure` behind HTTPS), `X-Xellter` header required on writes, CSP and frame denial.
- Request log has metadata only (client, model, tokens, cost, timing, error), no prompts or answers.
- Reset a lost login or rotate the authenticator: `xellter admin init -force -credentials-out data/FIRST-LOGIN.txt`, then restart the service (it ends all sessions and prints a new password, TOTP secret and recovery codes).
