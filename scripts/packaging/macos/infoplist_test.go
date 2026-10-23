// Package macos holds the macOS app bundles' packaging files; its test checks the Info.plist
// templates scripts/build-macos-apps.sh puts in both apps.
package macos

import (
	"encoding/xml"
	"os"
	"strings"
	"testing"
)

// plistKeys reads the top-level dict of a plist into key -> the value element's inner XML.
func plistKeys(t *testing.T, path string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Dict struct {
			Inner []byte `xml:",innerxml"`
		} `xml:"dict"`
	}
	if err := xml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	dec := xml.NewDecoder(strings.NewReader("<d>" + string(doc.Dict.Inner) + "</d>"))
	out := map[string]string{}
	var key string
	_, _ = dec.Token() // <d>
	for {
		tok, err := dec.Token()
		if err != nil {
			return out
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		var v struct {
			Inner string `xml:",innerxml"`
		}
		if err := dec.DecodeElement(&v, &se); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if se.Name.Local == "key" {
			key = v.Inner
			continue
		}
		out[key] = se.Name.Local + ":" + strings.Join(strings.Fields(v.Inner), " ")
	}
}

// Without these macOS's Local Network privacy silently blocks the app from the LAN (#480):
// the agent found no server at 10.0.0.122 while curl in Terminal did.
func TestBothAppsAskForTheLocalNetwork(t *testing.T) {
	for path, who := range map[string]string{
		"server/Info.plist": "Jarvis Automation Server ",
		"../../../internal/agent/deploy/macos/Info.plist": "Jarvis Automation Agent ",
	} {
		p := plistKeys(t, path)
		if d := p["NSLocalNetworkUsageDescription"]; !strings.HasPrefix(d, "string:"+who) || !strings.Contains(d, "local network") {
			t.Errorf("%s: NSLocalNetworkUsageDescription = %q", path, d)
		}
		if b := p["NSBonjourServices"]; b != "array:<string>_jarvis-config._tcp</string>" {
			t.Errorf("%s: NSBonjourServices = %q", path, b)
		}
	}
}
