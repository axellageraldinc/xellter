package admin

import (
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/axellageraldinc/xellter/internal/config"
)

// configDocs explains every key of the config file, by dotted path. A client's presets are named by the
// owner, so their level is written "*". The comments exist only in the text the Config tab shows: the file
// on disk is rewritten without them, and the next GET puts them back.
var configDocs = map[string]string{
	"server":                         "How the xellter process listens and who may open the panel.",
	"server.listen":                  "Address and port xellter binds to. Keep it on 127.0.0.1 and put tailscale serve or a reverse proxy in front. Needs a restart.",
	"server.data_dir":                "Folder for the SQLite database (request log, catalog cache, sessions), secrets and config history. Never delete it.",
	"server.secrets_file":            "File holding the secret values (kenari keys) that the key_env names point to.",
	"server.admin_hosts":             "Host headers allowed to open the panel. A request for any other host is refused; add a domain here when you expose a new one.",
	"server.max_body_mb":             "Largest request body xellter accepts, in MB. Raise it for big image or audio uploads.",
	"server.log_retention_days":      "How many days of request log (metadata only, never prompts) are kept before they are deleted.",
	"server.trusted_proxy":           "true = believe X-Forwarded-For and X-Forwarded-Proto from the proxy in front. Keep true behind tailscale serve or cloudflared; false if clients reach xellter directly.",
	"server.session_hours":           "How long a panel login lasts before you must sign in again.",
	"server.auth_record":             "Path of the admin login record (password hash, TOTP, recovery codes). Do not edit.",
	"kenari":                         "How xellter talks to kenari.id, the built-in provider (id kenari). Keys and models that name no provider belong to it.",
	"kenari.base_url":                "Root URL of the kenari API.",
	"kenari.sync_minutes":            "How often the model catalog, prices, promos and plans are re-downloaded from kenari.",
	"kenari.user_agent":              "User-Agent header sent to kenari.",
	"kenari.first_byte_timeout_s":    "Seconds to wait for kenari to start answering before the request is given up on.",
	"kenari.music_timeout_s":         "Same wait for music generation, which is much slower than chat.",
	"kenari.account_key_env":         "Name of the secret that holds your full-access kenari account key. It only reads plan, usage and balance; it never serves requests.",
	"providers":                      "Further upstreams besides kenari. A key names its provider; a model of one is listed in a pool as <id>/<model>. Add a key to it in the panel.",
	"providers.id":                   "Short name: lowercase letters, digits, dash. It is the prefix of the provider's models and may not be kenari.",
	"providers.type":                 "Wire protocol: kenari (the only one so far; it can point at any host that speaks kenari's API).",
	"providers.base_url":             "Root URL of the provider's API.",
	"providers.sync_minutes":         "How often this provider's model catalog is re-downloaded.",
	"providers.user_agent":           "User-Agent header sent to this provider.",
	"providers.first_byte_timeout_s": "Seconds to wait for this provider to start answering before the request is given up on.",
	"providers.music_timeout_s":      "Same wait for music generation.",
	"providers.account_key_env":      "Name of the secret holding a full-access key of the account at this provider; it only reads plan and balance.",
	"alerts":                         "Notices about spending. Nothing is ever blocked by them.",
	"alerts.spend_spike_factor":      "A client alerts when its last hour cost this many times its usual hour (the rest of the week). 0 or less switches the alert off.",
	"alerts.spend_spike_min_idr":     "An hour must cost at least this many rupiah at list price to alert, so a quiet client does not alert on pennies.",
	"alerts.webhook_url":             "Optional. Each new alert is POSTed here as JSON (client, rupiah figures, never a prompt), at most once per client per 6 hours. Empty: the panel shows it and nothing is sent.",
	"routing":                        "Routing is the same for every client and preset. These are the only knobs.",
	"routing.rank_refresh_minutes":   "Rankings are rebuilt on this interval (also after a catalog sync and on Recalculate). 0 = never on a timer.",
	"routing.top_models":             "How many of the best-ordered models of a preset requests are spread over (round-robin).",
	"routing.evict_errors":           "Errors inside evict_window_minutes that take a model out of ONE preset.",
	"routing.evict_window_minutes":   "The window in which evict_errors are counted.",
	"routing.evict_cooldown_minutes": "How long an evicted model is skipped in that preset. Also the ceiling for quarantine_minutes as it doubles.",
	"routing.avoid_sunset_days":      "A model that kenari retires within this many days is not offered.",
	"routing.first_token_timeout_s":  "A streamed chat answer silent this many seconds (plus 1 s per 10k prompt tokens) moves on to the next model. -1 = off.",
	"routing.slow_first_token_s":     "If a model's median wait for the first token is above this many seconds, it is taken out of the preset for a while.",
	"routing.fail_percent":           "If this share (percent) of a model's requests fail, it is taken out of the preset for a while.",
	"routing.health_min_requests":    "Requests a model must have served before slow_first_token_s and fail_percent are judged. Prevents one bad request from deciding.",
	"routing.quarantine_minutes":     "First time out for a slow or failing model. It doubles each time the model comes back and fails again, up to evict_cooldown_minutes.",
	"routing.repin_first_token_s":    "A session's pinned model this slow (first token, seconds) in 2 of the session's last 3 requests loses the session and is re-chosen.",
	"local_models":                   "Models served from this machine instead of kenari. Free, no key, and added to the pool of every preset of their category.",
	"local_models.id":                "Name a preset lists and the panel shows for this model.",
	"local_models.category":          "audio_transcription or audio_speech (the only two supported).",
	"local_models.base_url":          "Root of the local server, which must speak the OpenAI API.",
	"local_models.model":             "Optional: the name the local server knows the model by. Defaults to id.",
}

const configDocsHeader = "Comments are explanations only: they are regenerated on every load and dropped when you save."

// annotatedYAML renders cfg as YAML with configDocs as comments above each key.
func annotatedYAML(cfg *config.Config) ([]byte, error) {
	var doc yaml.Node
	internal := *cfg
	internal.Clients = nil // clients are edited in the panel and live in the database (ADR 0037)
	if err := doc.Encode(&internal); err != nil {
		return nil, err
	}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		doc.Content[0].HeadComment = configDocsHeader
	}
	annotate(&doc, "")
	return yaml.Marshal(&doc)
}

func annotate(n *yaml.Node, path string) {
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, c := range n.Content {
			annotate(c, path)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			p := strings.TrimPrefix(path+"."+k.Value, ".")
			if doc, ok := configDocs[p]; ok {
				k.HeadComment = doc
			}
			annotate(v, p)
		}
	}
}
