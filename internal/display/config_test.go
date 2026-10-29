package display

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigurationInheritance(t *testing.T) {
	c, err := MergeConfiguration(map[string]any{"idle_seconds": 80}, map[string]any{"clock": "combined"})
	if err != nil || c.IdleSeconds != 80 || c.Clock != "combined" || c.SlideSeconds != 30 || c.Enabled {
		t.Fatalf("%+v %v", c, err)
	}
	c, err = MergeConfiguration(map[string]any{"idle_seconds": 80}, map[string]any{"idle_seconds": nil})
	if err != nil || c.IdleSeconds != 80 {
		t.Fatal(c, err)
	}
	for _, v := range []map[string]any{{"clock": "bad"}, {"idle_seconds": -1}, {"timezone": "not/a/zone"}, {"unknown": 1}, {"shortcuts": []any{map[string]any{"name": "bad", "url": "file:///etc/passwd"}}}} {
		if _, e := MergeConfiguration(v, nil); e == nil {
			t.Fatal("accepted", v)
		}
	}
}
func TestPrivateAtomicCacheAndPIN(t *testing.T) {
	v, err := NewVerifier("123456")
	if err != nil {
		t.Fatal(err)
	}
	if CheckPIN(v, "123456") != nil || CheckPIN(v, "123455") == nil {
		t.Fatal("PIN check")
	}
	if _, err := NewVerifier("12345x"); err == nil {
		t.Fatal("invalid PIN")
	}
	for _, pin := range []string{"1234", "12345678901234567890"} {
		if _, err := NewVerifier(pin); err != nil {
			t.Fatal(err)
		}
	}
	for _, pin := range []string{"123", "123456789012345678901"} {
		if _, err := NewVerifier(pin); err == nil {
			t.Fatal("invalid length accepted")
		}
	}
	p := filepath.Join(t.TempDir(), "cache.json")
	if err := WriteCache(p, []byte(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0600 {
		t.Fatal(st.Mode())
	}
}
func TestThemeArchiveBoundary(t *testing.T) {
	pack := func(extra string) []byte {
		var b bytes.Buffer
		w := zip.NewWriter(&b)
		f, _ := w.Create("manifest.json")
		f.Write([]byte(`{"api_version":1,"name":"Starter","entry":"index.html"}`))
		f, _ = w.Create("index.html")
		f.Write([]byte("hello"))
		if extra != "" {
			f, _ = w.Create(extra)
			f.Write([]byte("bad"))
		}
		w.Close()
		return b.Bytes()
	}
	if _, err := ReadTheme(pack("")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"../escape", "/absolute", "a/../../escape", `a\b`, "index.html"} {
		if _, err := ReadTheme(pack(p)); err == nil {
			t.Fatal("accepted", p)
		}
	}
}

func TestWaveformAppearanceInheritanceAndValidation(t *testing.T) {
	c, err := MergeConfiguration(map[string]any{"waveform_colour": "#ff6600", "waveform_emphasis": 175}, map[string]any{"waveform_colour": "#00ccff"})
	if err != nil {
		t.Fatal(err)
	}
	if c.WaveformColour != "#00ccff" || c.WaveformEmphasis != 175 {
		t.Fatal(c)
	}
	c, err = MergeConfiguration(map[string]any{"waveform_colour": "#ff6600"}, map[string]any{"waveform_colour": nil})
	if err != nil || c.WaveformColour != "#ff6600" {
		t.Fatal(c, err)
	}
	for _, bad := range []map[string]any{{"waveform_colour": "red;url(https://example.org)"}, {"waveform_colour": "#12345"}, {"waveform_emphasis": 0}, {"waveform_emphasis": 201}} {
		if _, err := MergeConfiguration(bad); err == nil {
			t.Fatal("accepted invalid appearance", bad)
		}
	}
}

func TestLegacyCacheRevisionWithoutAppearanceFields(t *testing.T) {
	const legacy = `{"enabled":false,"theme":"builtin","favourites":[],"idle_seconds":120,"slide_seconds":30,"photos":[],"clock":"digital","hour_24":true,"timezone":"","night_dim":false,"dim_start":22,"dim_end":7,"dim_percent":35,"destination":"local","shortcuts":[],"playercast":false,"experimental_cast":false,"receiver_name":"Jarvis Display"}`
	c, err := MergeConfiguration(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.WaveformColour, c.WaveformEmphasis = "", 0
	if got, want := GetRevision(c, ""), Digest([]byte(`{"Config":`+legacy+`,"Verifier":""}`)); got != want {
		t.Fatal("older offline cache revision changed", got, want)
	}
}
