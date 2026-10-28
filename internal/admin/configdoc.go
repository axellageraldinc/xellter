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
	"kenari":                         "How xellter talks to kenari.id.",
	"kenari.base_url":                "Root URL of the kenari API.",
	"kenari.sync_minutes":            "How often the model catalog, prices, promos and plans are re-downloaded from kenari.",
	"kenari.user_agent":              "User-Agent header sent to kenari.",
	"kenari.first_byte_timeout_s":    "Seconds to wait for kenari to start answering before the request is given up on.",
	"kenari.music_timeout_s":         "Same wait for music generation, which is much slower than chat.",
	"kenari.account_key_env":         "Name of the secret that holds your full-access kenari account key. It only reads plan, usage and balance; it never serves requests.",
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
	"clients":                        "A client is one app that calls xellter, with its own kenari keys, one purpose and at most one preset per category.",
	"clients.name":                   "Client name shown in the panel.",
	"clients.enabled":                "false = the client's xellter key is refused. Nothing else is deleted.",
	"clients.purpose":                "How the chat preset is scored: general | coding | agentic | vision | bulk. A purpose reorders models, it never filters them.",
	"clients.key_hash":               "SHA-256 of the client's xellter key. Created by the panel; do not edit.",
	"clients.key_hint":               "Last characters of the xellter key, so you can recognise it. Do not edit.",
	"clients.keys":                   "kenari API keys this client spends from.",
	"clients.keys.name":              "Name of the key in the panel.",
	"clients.keys.key_env":           "Name of the secret in the secrets file that holds the key value.",
	"clients.keys.capability":        "Empty = a normal model key. \"search\" = serves only POST /v1/x/search, is never probed and never serves a model call.",
	"clients.presets":                "One preset per category. The preset's name is the model name the app asks for.",
	"clients.presets.*.category":     "What the preset serves: chat | images | videos | audio_speech | audio_transcription | music | systemone. A client holds one preset of each.",
	"clients.presets.*.optimization": "How the pool is ordered: cheapest | balanced | best_quality.",
	"clients.presets.*.models":       "The pool of catalog models this preset may route to. Free and discounted models of the category join on their own.",
	"clients.presets.*.excluded":     "Free or discounted models you unticked; they are never added to this preset again.",
	"clients.presets.*.sticky":       "true (or absent) = a session keeps one model; false = every turn is routed again.",
	"clients.note":                   "Free text for yourself.",
}

const configDocsHeader = "Comments are explanations only: they are regenerated on every load and dropped when you save."

// annotatedYAML renders cfg as YAML with configDocs as comments above each key.
func annotatedYAML(cfg *config.Config) ([]byte, error) {
	var doc yaml.Node
	if err := doc.Encode(cfg); err != nil {
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
			seg := k.Value
			if path == "clients.presets" {
				seg = "*"
			}
			p := strings.TrimPrefix(path+"."+seg, ".")
			if doc, ok := configDocs[p]; ok && seg != "*" {
				k.HeadComment = doc
			}
			annotate(v, p)
		}
	}
}
