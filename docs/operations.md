# Operations

## First install
```bash
make build
install -d -m 700 data
./bin/xellter admin init -credentials-out data/FIRST-LOGIN.txt     # username, password, TOTP secret, recovery codes
./bin/xellter client add -name babu -out data/babu.key             # the xellter key the app will use
echo 'kn-...' | ./bin/xellter key add -client babu -name main      # a kenari key for that client
./bin/xellter serve
```
The same can be done in the panel (Clients, Add client, Add key). `admin init -from-record <file>` reuses an existing login record instead of generating one.

## systemd (user)
`~/infra/systemd/user/xellter.service` runs `bin/xellter serve -config xellter.yaml` from the project directory. After editing: `systemctl --user daemon-reload`. Logs: `journalctl --user -u xellter -f`.

## Tailscale
```bash
tailscale serve --bg --https=8445 http://127.0.0.1:8480
```
The tailnet URL is `https://<host>.<tailnet>.ts.net:8445/`; add that host name to `server.admin_hosts` to open `/admin/`. Remove with `tailscale serve --https=8445 off`.

## Public domain (later)
Expose only what clients need. Because `/admin` answers only for hostnames listed in `admin_hosts`, a public hostname that is not in that list serves `/v1/*` and `/healthz` and returns 404 for the panel. Still prefer Cloudflare Access or a path filter in front of the public name.

## Day to day
- **Overview:** health banner with warnings you can act on, 24 h numbers, plan budget and balance, per-client summary.
- **Clients -> a client:** its kenari keys (add, replace, remove, settings), its presets (what they serve now, "Optimise for", requirements; "Why this order?" explains the ranking), its spending limit.
- **Add a client:** Clients, Add client. The xellter key is shown once. Then Add key on the client page; presets appear a few seconds after the key is checked.
- **Rotate a client key:** client page, New xellter key (the old key dies immediately).
- **Plan numbers:** Overview, Kenari plan & balance, Add account key (a full-access kenari key). Without it the plan budget is estimated.
- **Usage:** per client, preset, model or kenari key, with a request list; filter by client and period.
- **A client got `503`/`429`:** the Overview banner names the key or account that is paused and why; the client page has "Re-check".
- Backup: `data/` (SQLite is WAL: `sqlite3 data/xellter.db ".backup file"`) and `xellter.yaml`.

## Security model
- Client keys: random 192-bit, stored as sha256, shown once; a key belongs to one client.
- Upstream key values: only in `data/secrets.env` (0600), never in config, logs or responses.
- Admin: Host allowlist, scrypt (N=2^17) + mandatory TOTP, single-use recovery codes, 5 attempts/15 min per client and 30 global, `HttpOnly` + `SameSite=Strict` cookie (`Secure` behind HTTPS), `X-Xellter` header required on writes, CSP and frame denial.
- Request log has metadata only (client, model, tokens, cost, timing, error), no prompts or answers.
- Reset a lost login or rotate the authenticator: `xellter admin init -force -credentials-out data/FIRST-LOGIN.txt`, then restart the service (it ends all sessions and prints a new password, TOTP secret and recovery codes).
