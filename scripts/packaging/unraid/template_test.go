package unraid

import (
	"bytes"
	"encoding/xml"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type config struct {
	Name        string `xml:"Name,attr"`
	Target      string `xml:"Target,attr"`
	Default     string `xml:"Default,attr"`
	Mode        string `xml:"Mode,attr"`
	Description string `xml:"Description,attr"`
	Type        string `xml:"Type,attr"`
	Display     string `xml:"Display,attr"`
	Required    string `xml:"Required,attr"`
	Value       string `xml:",chardata"`
}

type container struct {
	XMLName     xml.Name `xml:"Container"`
	Version     string   `xml:"version,attr"`
	Name        string   `xml:"Name"`
	Repository  string   `xml:"Repository"`
	Registry    string   `xml:"Registry"`
	Network     string   `xml:"Network"`
	Privileged  string   `xml:"Privileged"`
	Shell       string   `xml:"Shell"`
	Support     string   `xml:"Support"`
	Project     string   `xml:"Project"`
	Overview    string   `xml:"Overview"`
	Category    string   `xml:"Category"`
	WebUI       string   `xml:"WebUI"`
	TemplateURL string   `xml:"TemplateURL"`
	Icon        string   `xml:"Icon"`
	ExtraParams string   `xml:"ExtraParams"`
	Configs     []config `xml:"Config"`
}

func TestTemplate(t *testing.T) {
	raw, err := os.ReadFile("jarvis.xml")
	if err != nil {
		t.Fatalf("read jarvis.xml: %v", err)
	}
	var c container
	if err := xml.Unmarshal(raw, &c); err != nil {
		t.Fatalf("unmarshal jarvis.xml: %v", err)
	}

	if c.Repository != "ghcr.io/alexberardi/jarvisd:latest" {
		t.Errorf("Repository = %q", c.Repository)
	}
	if c.Network != "bridge" {
		t.Errorf("Network = %q", c.Network)
	}
	if !strings.Contains(c.WebUI, "[PORT:7710]") {
		t.Errorf("WebUI = %q", c.WebUI)
	}

	var ports []string
	for _, cfg := range c.Configs {
		if cfg.Type == "Port" {
			ports = append(ports, cfg.Target)
		}
	}
	want := map[string]bool{"7700": true, "7710": true, "1884": true}
	if len(ports) != 3 {
		t.Fatalf("got %d Port configs, want 3: %v", len(ports), ports)
	}
	for _, p := range ports {
		if !want[p] {
			t.Errorf("unexpected or repeated port target %q", p)
		}
		delete(want, p)
	}
	for p := range want {
		t.Errorf("missing port target %q", p)
	}

	foundPath := false
	for _, cfg := range c.Configs {
		if cfg.Type != "Path" {
			continue
		}
		foundPath = true
		if cfg.Target != "/data" || cfg.Default != "/mnt/user/appdata/jarvis" {
			t.Errorf("Path config = target %q default %q", cfg.Target, cfg.Default)
		}
	}
	if !foundPath {
		t.Error("no Path config")
	}

	foundContainer := false
	for _, cfg := range c.Configs {
		if cfg.Type == "Variable" && cfg.Name == "JARVIS_CONTAINER" {
			foundContainer = true
			if cfg.Value != "unraid" {
				t.Errorf("JARVIS_CONTAINER = %q", cfg.Value)
			}
		}
	}
	if !foundContainer {
		t.Error("no JARVIS_CONTAINER variable")
	}

	for _, cfg := range c.Configs {
		if cfg.Required == "true" && cfg.Default == "" {
			t.Errorf("required config %s has an empty default", cfg.Name)
		}
	}
	for _, cfg := range c.Configs {
		if cfg.Name == "NVIDIA_VISIBLE_DEVICES" && cfg.Required != "false" {
			t.Errorf("NVIDIA_VISIBLE_DEVICES Required = %q, want false", cfg.Required)
		}
		// Set, it overrides server.legacy_ports, which import-legacy turns on for older nodes.
		if cfg.Name == "JARVIS_LEGACY_PORTS" && (cfg.Default != "" || cfg.Value != "") {
			t.Errorf("JARVIS_LEGACY_PORTS = default %q value %q, want both empty", cfg.Default, cfg.Value)
		}
	}
	if strings.Contains(c.Overview, "phones and nodes find") || strings.Contains(c.Overview, "discovery for phones") {
		t.Errorf("Overview says phones find the server over mDNS; only the desktop agent does: %q", c.Overview)
	}

	icon, err := os.ReadFile(filepath.Join("jarvis.png"))
	if err != nil {
		t.Fatalf("read jarvis.png: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(icon))
	if err != nil {
		t.Fatalf("decode jarvis.png: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != 128 || b.Dy() != 128 {
		t.Errorf("jarvis.png is %dx%d, want 128x128", b.Dx(), b.Dy())
	}

	if !strings.HasSuffix(c.Icon, "/scripts/packaging/unraid/jarvis.png") {
		t.Errorf("Icon = %q", c.Icon)
	}
}
