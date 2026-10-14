package nodesettings

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// golden is testdata/k2_golden.json, written by the Python node's own encrypt_snapshot and
// _decrypt_config (tools/golden/export_k2.py).
type golden struct {
	K2        string `json:"k2"`
	NodeID    string `json:"node_id"`
	Snapshots []struct {
		Plaintext string `json:"plaintext"`
		Sealed
		SchemaVersion         int `json:"aad_schema_version"`
		CommandsSchemaVersion int `json:"aad_commands_schema_version"`
		Revision              int `json:"aad_revision"`
	} `json:"snapshots"`
	Pushes []struct {
		ConfigType string `json:"config_type"`
		Sealed
		JSON map[string]any `json:"json"`
	} `json:"pushes"`
}

func loadGolden(t *testing.T) golden {
	t.Helper()
	b, err := os.ReadFile("testdata/k2_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var g golden
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	if len(g.Snapshots) == 0 || len(g.Pushes) == 0 {
		t.Fatal("empty golden")
	}
	return g
}

// The agent seals a snapshot to exactly the bytes the Python node produces for the same
// plaintext and nonce, so the mobile app can't tell them apart.
func TestSealMatchesThePythonNode(t *testing.T) {
	g := loadGolden(t)
	key, err := DecodeKey(g.K2)
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range g.Snapshots {
		nonce, _ := unb64(s.Nonce)
		got, err := Seal(key, g.NodeID, SnapshotConfigType, []byte(s.Plaintext), nonce)
		if err != nil {
			t.Fatal(err)
		}
		if got != s.Sealed {
			t.Errorf("snapshot %d: sealed %+v, Python sealed %+v", i, got, s.Sealed)
		}
		if s.SchemaVersion != SchemaVersion || s.CommandsSchemaVersion != CommandsSchemaVersion || s.Revision != 1 {
			t.Errorf("snapshot %d: aad versions %d/%d/%d", i, s.SchemaVersion, s.CommandsSchemaVersion, s.Revision)
		}
		pt, err := Open(key, g.NodeID, SnapshotConfigType, s.Sealed)
		if err != nil || string(pt) != s.Plaintext {
			t.Errorf("snapshot %d: round trip = %q, %v", i, pt, err)
		}
	}
}

// Config pushes in the mobile app's format, which the Python node's _decrypt_config accepted
// when the golden was written, decrypt to the same JSON here.
func TestOpenReadsMobilePushes(t *testing.T) {
	g := loadGolden(t)
	key, _ := DecodeKey(g.K2)
	for i, p := range g.Pushes {
		pt, err := Open(key, g.NodeID, p.ConfigType, p.Sealed)
		if err != nil {
			t.Fatalf("push %d (%s): %v", i, p.ConfigType, err)
		}
		var got map[string]any
		if err := json.Unmarshal(pt, &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, p.JSON) {
			t.Errorf("push %d: %v, want %v", i, got, p.JSON)
		}
		// The AAD binds node and type: either one changed is refused.
		if _, err := Open(key, "other-node", p.ConfigType, p.Sealed); err != ErrDecrypt {
			t.Errorf("push %d for another node: %v", i, err)
		}
		if _, err := Open(key, g.NodeID, p.ConfigType+"x", p.Sealed); err != ErrDecrypt {
			t.Errorf("push %d as another type: %v", i, err)
		}
	}
}

func TestDecodeKeyInsistsOn32Bytes(t *testing.T) {
	if _, err := DecodeKey(b64(make([]byte, 16))); err == nil {
		t.Fatal("16-byte key accepted")
	}
	if _, err := DecodeKey("not base64!"); err == nil {
		t.Fatal("bad encoding accepted")
	}
	// Padded and unpadded both decode.
	if _, err := DecodeKey(b64(make([]byte, 32)) + "="); err != nil {
		t.Fatal(err)
	}
}
