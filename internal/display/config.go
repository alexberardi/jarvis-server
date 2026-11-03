// Package display defines the shared, versioned touch display configuration and asset format.
package display

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const MaxAssetBytes = 20 << 20

type Shortcut struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}
type Configuration struct {
	WaveformColour   string     `json:"waveform_colour,omitempty"`
	WaveformEmphasis int        `json:"waveform_emphasis,omitempty"`
	Enabled          bool       `json:"enabled"`
	Theme            string     `json:"theme"`
	Favourites       []string   `json:"favourites"`
	IdleSeconds      int        `json:"idle_seconds"`
	SlideSeconds     int        `json:"slide_seconds"`
	Photos           []string   `json:"photos"`
	Clock            string     `json:"clock"`
	Hour24           bool       `json:"hour_24"`
	Timezone         string     `json:"timezone"`
	NightDim         bool       `json:"night_dim"`
	DimStart         int        `json:"dim_start"`
	DimEnd           int        `json:"dim_end"`
	DimPercent       int        `json:"dim_percent"`
	Destination      string     `json:"destination"`
	Shortcuts        []Shortcut `json:"shortcuts"`
	Playercast       bool       `json:"playercast"`
	ExperimentalCast bool       `json:"experimental_cast"`
	ReceiverName     string     `json:"receiver_name"`
}
type Delivery struct {
	Configuration Configuration `json:"configuration"`
	Revision      string        `json:"revision"`
	Verifier      string        `json:"verifier"`
}

func MergeConfiguration(layers ...map[string]any) (Configuration, error) {
	c := Configuration{WaveformColour: "#a56bff", WaveformEmphasis: 150, Theme: "builtin", Favourites: []string{}, IdleSeconds: 120, SlideSeconds: 30, Photos: []string{}, Clock: "digital", Hour24: true, Destination: "local", Shortcuts: []Shortcut{}, ReceiverName: "Jarvis Display", DimStart: 22, DimEnd: 7, DimPercent: 35}
	b, _ := json.Marshal(c)
	var merged map[string]any
	_ = json.Unmarshal(b, &merged)
	for _, l := range layers {
		for k, v := range l {
			if _, ok := merged[k]; !ok {
				return c, fmt.Errorf("unknown display setting %q", k)
			}
			if v != nil {
				merged[k] = v
			}
		}
	}
	b, err := json.Marshal(merged)
	if err != nil {
		return c, err
	}
	if err = json.Unmarshal(b, &c); err != nil {
		return c, errors.New("invalid display setting type")
	}
	if c.IdleSeconds < 10 || c.IdleSeconds > 86400 || c.SlideSeconds < 5 || c.SlideSeconds > 3600 || c.DimStart < 0 || c.DimStart > 23 || c.DimEnd < 0 || c.DimEnd > 23 || c.DimPercent < 10 || c.DimPercent > 100 {
		return c, errors.New("display timing or dimming is out of range")
	}
	if !regexp.MustCompile(`^#[0-9a-fA-F]{6}$`).MatchString(c.WaveformColour) || c.WaveformEmphasis < 25 || c.WaveformEmphasis > 200 {
		return c, errors.New("waveform colour must be #RRGGBB and emphasis must be 25 to 200 percent")
	}
	if c.Clock != "digital" && c.Clock != "analogue" && c.Clock != "combined" && c.Clock != "photos" {
		return c, errors.New("invalid clock format")
	}
	if c.Timezone != "" {
		if _, err = time.LoadLocation(c.Timezone); err != nil {
			return c, errors.New("unknown timezone")
		}
	}
	if len(c.Favourites) > 500 || len(c.Photos) > 100 || len(c.Shortcuts) > 30 || len(c.ReceiverName) < 1 || len(c.ReceiverName) > 60 || strings.ContainsAny(c.ReceiverName, "\x00\r\n") {
		return c, errors.New("display list or name limit exceeded")
	}
	if c.Theme != "builtin" && !ValidAssetID(c.Theme) {
		return c, errors.New("invalid theme ID")
	}
	for _, p := range c.Photos {
		if !ValidAssetID(p) {
			return c, errors.New("invalid photo ID")
		}
	}
	if len(c.Destination) > 200 {
		return c, errors.New("invalid playback destination")
	}
	for _, s := range c.Shortcuts {
		if len(s.Name) < 1 || len(s.Name) > 80 || !WebURL(s.URL) {
			return c, errors.New("shortcuts require a name and HTTP(S) URL")
		}
	}
	return c, nil
}
func WebURL(s string) bool {
	u, e := url.Parse(s)
	return e == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil && len(s) <= 4096
}
func ValidAssetID(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && s == strings.ToLower(s)
}
func Digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func GetRevision(c Configuration, v string) string {
	b, _ := json.Marshal(struct {
		Config   Configuration
		Verifier string
	}{c, v})
	return Digest(b)
}

var pinPattern = regexp.MustCompile(`^[0-9]{4,20}$`)

func NewVerifier(pin string) (string, error) {
	if !pinPattern.MatchString(pin) {
		return "", errors.New("exit PIN must contain 4 to 20 digits")
	}
	b, e := bcrypt.GenerateFromPassword([]byte(pin), 12)
	return string(b), e
}
func CheckPIN(verifier, pin string) error {
	if !pinPattern.MatchString(pin) {
		return errors.New("invalid PIN")
	}
	return bcrypt.CompareHashAndPassword([]byte(verifier), []byte(pin))
}
func WriteCache(filename string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(filename), ".display-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	return os.Rename(name, filename)
}

type Manifest struct {
	APIVersion int    `json:"api_version"`
	Name       string `json:"name"`
	Entry      string `json:"entry"`
}
type Theme struct {
	Manifest Manifest
	Files    map[string][]byte
}

func ReadTheme(b []byte) (Theme, error) {
	t := Theme{Files: map[string][]byte{}}
	if len(b) > MaxAssetBytes {
		return t, errors.New("theme exceeds 20 MiB")
	}
	z, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return t, err
	}
	if len(z.File) > 256 {
		return t, errors.New("too many theme files")
	}
	total := 0
	for _, f := range z.File {
		n := f.Name
		if strings.ContainsAny(n, "\\\x00") || strings.Contains(n, ":") || strings.HasPrefix(n, "/") || path.Clean(strings.TrimSuffix(n, "/")) != strings.TrimSuffix(n, "/") || n == ".." || strings.HasPrefix(n, "../") || f.Mode()&os.ModeSymlink != 0 {
			return t, errors.New("unsafe theme path")
		}
		if f.FileInfo().IsDir() {
			continue
		}
		if _, ok := t.Files[n]; ok {
			return t, errors.New("duplicate theme path")
		}
		rd, e := f.Open()
		if e != nil {
			return t, e
		}
		data, e := io.ReadAll(io.LimitReader(rd, MaxAssetBytes+1))
		rd.Close()
		total += len(data)
		if e != nil || total > MaxAssetBytes {
			return t, errors.New("theme expanded size exceeds 20 MiB")
		}
		t.Files[n] = data
	}
	if err = json.Unmarshal(t.Files["manifest.json"], &t.Manifest); err != nil {
		return t, errors.New("invalid theme manifest")
	}
	if t.Manifest.APIVersion != 1 || len(t.Manifest.Name) < 1 || len(t.Manifest.Name) > 80 || !strings.HasSuffix(t.Manifest.Entry, ".html") || t.Files[t.Manifest.Entry] == nil {
		return t, errors.New("unsupported theme API or missing entry")
	}
	return t, nil
}
