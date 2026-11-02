// Package devicetypes is the SDK's device-type catalogue (jarvis-command-sdk device_types.json),
// embedded: per-domain icon, control type, UI hints, buttons and state fields. The copy here must
// equal the pinned SDK's (sidecar's TestCatalogueMatchesTheSDK); change it in the SDK first.
package devicetypes

import (
	_ "embed"
	"encoding/json"
)

//go:embed device_types.json
var raw []byte

// Action is one button: what the apps show and the action name control_device runs.
type Action struct {
	Text   string `json:"text"`
	Action string `json:"action"`
	Type   string `json:"type"`
	Icon   string `json:"icon"`
}

// Type is one domain's entry.
type Type struct {
	Label        string         `json:"label"`
	Icon         string         `json:"icon"`
	Visible      bool           `json:"visible"`
	Controllable bool           `json:"controllable"`
	ControlType  string         `json:"control_type"`
	Features     []string       `json:"features"`
	UIHints      map[string]any `json:"ui_hints"`
	StateFields  []string       `json:"state_fields"`
	Actions      []Action       `json:"actions"`
}

var catalogue = func() (c struct {
	Version  int             `json:"version"`
	Domains  map[string]Type `json:"domains"`
	Fallback Type            `json:"fallback"`
}) {
	if err := json.Unmarshal(raw, &c); err != nil {
		panic("devicetypes: " + err.Error())
	}
	return c
}()

// Lookup is domain's type and whether the catalogue knows it (else the fallback).
func Lookup(domain string) (Type, bool) {
	t, ok := catalogue.Domains[domain]
	if !ok {
		return catalogue.Fallback, false
	}
	return t, true
}

// JSON is the catalogue file as embedded.
func JSON() []byte { return raw }
