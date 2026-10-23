package unraid

import (
	"bytes"
	"encoding/xml"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	pconfig "github.com/alexberardi/jarvis-server/internal/platform/config"
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
	// A migrating user maps every legacy listener by hand: all but config (7700, already
	// mapped) and admin (7710), plus the MQTT WebSocket.
	var legacy []string
	for name, port := range pconfig.DefaultPorts {
		if name != pconfig.ListenerConfig && name != pconfig.ListenerAdmin {
			legacy = append(legacy, strconv.Itoa(port))
		}
	}
	legacy = append(legacy, strings.TrimPrefix(pconfig.DefaultMQTTWSAddr, ":"))
	var legacyDesc string
	for _, cfg := range c.Configs {
		if cfg.Name == "JARVIS_LEGACY_PORTS" {
			legacyDesc = cfg.Description
		}
	}
	for _, text := range []struct{ where, s string }{{"Overview", c.Overview}, {"JARVIS_LEGACY_PORTS", legacyDesc}} {
		for _, p := range legacy {
			if !regexp.MustCompile(`\b` + p + `\b`).MatchString(text.s) {
				t.Errorf("%s does not name legacy port %s: %q", text.where, p, text.s)
			}
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

	// Community Applications review: every link is https, and each category token ends in ':'.
	for _, link := range []struct{ field, url string }{
		{"Support", c.Support}, {"Project", c.Project}, {"Registry", c.Registry},
		{"TemplateURL", c.TemplateURL}, {"Icon", c.Icon},
	} {
		if !strings.HasPrefix(link.url, "https://") {
			t.Errorf("%s = %q, want an https URL", link.field, link.url)
		}
	}
	cats := strings.Fields(c.Category)
	if len(cats) == 0 {
		t.Error("no Category")
	}
	// CA writes categories "Parent:" or "Parent:Child" (Tools:Utilities).
	catRE := regexp.MustCompile(`^[A-Za-z]+:([A-Za-z]+)?$`)
	for _, cat := range cats {
		if !catRE.MatchString(cat) {
			t.Errorf("Category token %q is not Parent: or Parent:Child", cat)
		}
	}
	// The template's network is bridge; the Overview may say how to switch to Host, but must
	// not talk about host networking as if the template had it.
	if strings.Contains(strings.ToLower(c.Overview), "host network") {
		t.Errorf("Overview reads as if the template used host networking: %q", c.Overview)
	}
}

type maintainer struct {
	XMLName xml.Name `xml:"Maintainer"`
	Icon    string   `xml:"Icon"`
	Profile string   `xml:"Profile"`
	WebPage string   `xml:"WebPage"`
}

func TestCAProfile(t *testing.T) {
	raw, err := os.ReadFile("ca_profile.xml")
	if err != nil {
		t.Fatalf("read ca_profile.xml: %v", err)
	}
	var m maintainer
	if err := xml.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal ca_profile.xml: %v", err)
	}
	if strings.TrimSpace(m.Profile) == "" {
		t.Error("empty Profile")
	}
	for _, link := range []struct{ field, url string }{{"Icon", m.Icon}, {"WebPage", m.WebPage}} {
		if !strings.HasPrefix(link.url, "https://") {
			t.Errorf("%s = %q, want an https URL", link.field, link.url)
		}
	}
}

// The <Project> and <WebPage> links are the only ones a CA reviewer or an Unraid user clicks
// without the container running, so they must point at a page that resolves. The apex
// jarvisautomation.io has no A record (checked 2026-10-22), so pin the hosts to the known-live
// ones; TestProjectLinksLive re-checks the full URLs against the network.
func TestProjectLinkHosts(t *testing.T) {
	allowed := map[string]bool{"docs.jarvisautomation.dev": true, "github.com": true}
	raw, err := os.ReadFile("jarvis.xml")
	if err != nil {
		t.Fatalf("read jarvis.xml: %v", err)
	}
	var c container
	if err := xml.Unmarshal(raw, &c); err != nil {
		t.Fatalf("unmarshal jarvis.xml: %v", err)
	}
	raw, err = os.ReadFile("ca_profile.xml")
	if err != nil {
		t.Fatalf("read ca_profile.xml: %v", err)
	}
	var m maintainer
	if err := xml.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal ca_profile.xml: %v", err)
	}
	for _, link := range []struct{ field, url string }{{"Project", c.Project}, {"WebPage", m.WebPage}} {
		u, err := url.Parse(link.url)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			t.Errorf("%s = %q, want an https URL", link.field, link.url)
			continue
		}
		if !allowed[u.Hostname()] {
			t.Errorf("%s = %q, host %q is not in %v (the apex jarvisautomation.io does not resolve)",
				link.field, link.url, u.Hostname(), allowed)
		}
	}
}

// TestProjectLinksLive GETs the <Project> and <WebPage> URLs and expects a 200:
//
//	JARVIS_UNRAID_LIVE=1 go test -run Live ./scripts/packaging/unraid/
//
// It needs open network access, so it is skipped unless JARVIS_UNRAID_LIVE=1.
func TestProjectLinksLive(t *testing.T) {
	if os.Getenv("JARVIS_UNRAID_LIVE") != "1" {
		t.Skip("set JARVIS_UNRAID_LIVE=1 to check the project links are reachable")
	}
	raw, err := os.ReadFile("jarvis.xml")
	if err != nil {
		t.Fatalf("read jarvis.xml: %v", err)
	}
	var c container
	if err := xml.Unmarshal(raw, &c); err != nil {
		t.Fatalf("unmarshal jarvis.xml: %v", err)
	}
	raw, err = os.ReadFile("ca_profile.xml")
	if err != nil {
		t.Fatalf("read ca_profile.xml: %v", err)
	}
	var m maintainer
	if err := xml.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal ca_profile.xml: %v", err)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	for _, link := range []struct{ field, url string }{{"Project", c.Project}, {"WebPage", m.WebPage}} {
		req, err := http.NewRequest(http.MethodGet, link.url, nil)
		if err != nil {
			t.Fatalf("%s: %v", link.field, err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Errorf("%s %s: %v", link.field, link.url, err)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s %s: status %d, want 200", link.field, link.url, resp.StatusCode)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
}
