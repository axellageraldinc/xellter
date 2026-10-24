# Operations

## First install
```bash
make build
cp config.example.yaml xellter.yaml            # edit upstreams, admin_hosts
install -d -m 700 data
printf 'KENARI_KEY_X=kn-...\n' > data/secrets.env && chmod 600 data/secrets.env
./bin/xellter admin init -credentials-out data/FIRST-LOGIN.txt      # or: -from-record <aixell auth-record.json>
./bin/xellter client add -name babu -out data/babu.key
./bin/xellter serve
```
`admin init -from-record` reuses an existing record (same password and authenticator app as the source).

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
- Panel: Overview (key quotas, cooldowns), Shadow models ("Why this order?"), Usage (requests, cost), Config (history, restore).
- Add a client: panel -> Clients -> New client, or `xellter client add`. The key is shown once.
- Rotate a client key: panel -> Clients -> Rotate key (old key dies immediately).
- New kenari key: panel -> Upstreams -> Add key, Save, then "Set key value"; it is probed automatically.
- A client got `503`/`429`: Overview shows which key is cooling down and why; "Clear cooldowns" resets state.
- Backup: `data/` (SQLite is WAL: copy with `sqlite3 data/xellter.db ".backup file"`), `xellter.yaml`.

## Security model
- Client keys: random 192-bit, stored as sha256, shown once; a key belongs to one client.
- Upstream key values: only in `data/secrets.env` (0600), never in config, logs or responses.
- Admin: Host allowlist, scrypt (N=2^17) + mandatory TOTP, single-use recovery codes, 5 attempts/15 min per client and 30 global, `HttpOnly` + `SameSite=Strict` cookie (`Secure` behind HTTPS), `X-Xellter` header required on writes, CSP and frame denial.
- Request log has metadata only (client, model, tokens, cost, timing, error), no prompts or answers.
- Reset a lost login: stop nothing; delete `data/auth-record.json` and run `xellter admin init` again, then restart.
