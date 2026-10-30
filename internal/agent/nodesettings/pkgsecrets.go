package nodesettings

import (
	"context"
	"net/http"
)

// TopicPackageSecrets is jarvisd's nudge that the household's server-managed package secrets
// changed (cc packages_secrets.go). The message carries nothing secret; the node pulls.
const TopicPackageSecrets = "package-secrets"

// PackageSecrets pulls the secrets jarvisd holds for this node and stores them, as the app's
// K2 push does: the node's secret store is the same, so either path can set or change a
// secret. A value jarvisd returns as "" is a deleted secret and is cleared. It runs on the
// nudge and on every MQTT (re)connect (the backstop), so a node that was away catches up.
// Unlike the K2 push this needs no key: the pull rides the node's own authenticated channel,
// and works before the app has ever provisioned K2.
func (h *Handler) PackageSecrets(ctx context.Context) {
	var got struct {
		Secrets map[string]string `json:"secrets"`
	}
	if err := h.API.Do(ctx, http.MethodGet, h.path("/package-secrets"), nil, &got); err != nil {
		h.Log.Warn("settings: package secrets pull failed", "err", err)
		return
	}
	changed := 0
	for k, v := range got.Secrets {
		scope := h.Store.ScopeOf(k)
		if scope == "" {
			scope = "integration"
		}
		if scope == "user" || h.Store.Secret(k, scope, nil) == v {
			continue // a user's own secret is theirs, not the household's; unchanged is left alone
		}
		if err := h.Store.SetSecret(k, v, scope, "", nil); err != nil {
			h.Log.Warn("settings: package secret not stored", "key", k, "err", err)
			continue
		}
		changed++
	}
	if changed > 0 {
		h.Log.Info("settings: package secrets applied", "count", changed)
		if h.SecretsChanged != nil {
			h.SecretsChanged()
		}
	}
}
