package nodesettings

import "encoding/json"

// The snapshot's schema versions (settings_snapshot_service.py SCHEMA_VERSION,
// COMMANDS_SCHEMA_VERSION); they also go out as the upload's aad_* fields, with revision 1.
const (
	SchemaVersion         = 1
	CommandsSchemaVersion = 2
)

// SecretSpec is one IJarvisSecret a command declares (jarvis-command-sdk secret.py), as the
// sidecar describes it.
type SecretSpec struct {
	Key          string                       `json:"key"`
	Scope        string                       `json:"scope"`
	Description  string                       `json:"description"`
	ValueType    string                       `json:"value_type"`
	Required     bool                         `json:"required"`
	IsSensitive  bool                         `json:"is_sensitive"`
	FriendlyName string                       `json:"friendly_name,omitempty"`
	EnumValues   []string                     `json:"enum_values,omitempty"`
	Presets      map[string]map[string]string `json:"presets,omitempty"`
}

// Command is one command this node can run, for the snapshot: the agent's built-in tools
// (no secrets) and the sidecar's packages.
type Command struct {
	Name        string
	Description string
	Secrets     []SecretSpec
	// AssociatedService groups a package's commands under one service in the app (its
	// Services tab, and the unit "Sync to other nodes" copies); SetupGuide is shown there.
	AssociatedService string
	SetupGuide        json.RawMessage
}

// secretsList is _build_secrets_list (honor_include_values=True): every declared secret with
// is_set, and its value when it isn't sensitive or the request asked for values. A user-scoped
// secret is looked up for the requesting user.
func (st *Store) secretsList(specs []SecretSpec, includeValues bool, uid *int64) []map[string]any {
	out := []map[string]any{}
	for _, s := range specs {
		value := st.Secret(s.Key, s.Scope, uid)
		e := map[string]any{
			"key": s.Key, "scope": s.Scope, "description": s.Description, "value_type": s.ValueType,
			"required": s.Required, "is_sensitive": s.IsSensitive, "is_set": value != "",
		}
		if s.FriendlyName != "" {
			e["friendly_name"] = s.FriendlyName
		}
		if len(s.EnumValues) > 0 {
			e["enum_values"] = s.EnumValues
		}
		if len(s.Presets) > 0 {
			e["presets"] = s.Presets
		}
		if value != "" && (!s.IsSensitive || includeValues) {
			e["value"] = value
		}
		out = append(out, e)
	}
	return out
}

// DesktopNodeConfig is a computer's node_config: none of a Pi's hardware (the app hides the HAT
// card on hat_detected false). The Pi's voice and maintenance keys are left out: they are the
// Pi's, and the agent ignores pushes of them.
func DesktopNodeConfig() map[string]any {
	return map[string]any{
		"hardware": map[string]any{"hat_detected": false, "led_chain_available": false, "button_available": false},
	}
}

func (h *Handler) nodeConfig() map[string]any {
	if h.NodeConfig != nil {
		return h.NodeConfig()
	}
	return DesktopNodeConfig()
}

// Snapshot is build_snapshot for the agent: its commands with their secrets and enabled state;
// no background agents, device families or managers; and nodeConfig as node_config
// (DesktopNodeConfig on a computer, the HAT and voice settings on a Pi).
func (st *Store) Snapshot(cmds []Command, nodeConfig map[string]any, includeValues bool, uid *int64) map[string]any {
	entries := []map[string]any{}
	for _, c := range cmds {
		e := map[string]any{
			"command_name": c.Name, "description": c.Description,
			"secrets": st.secretsList(c.Secrets, includeValues, uid), "enabled": st.Enabled(c.Name),
		}
		if c.AssociatedService != "" {
			e["associated_service"] = c.AssociatedService
		}
		if g := c.SetupGuide; len(g) > 0 && string(g) != "null" {
			e["setup_guide"] = g
		}
		entries = append(entries, e)
	}
	return map[string]any{
		"schema_version":          SchemaVersion,
		"commands_schema_version": CommandsSchemaVersion,
		"commands":                entries,
		"agents":                  []any{},
		"device_families":         []any{},
		"device_managers":         []any{},
		"node_config":             nodeConfig,
	}
}
