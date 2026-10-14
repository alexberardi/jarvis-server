package apps

import (
	"errors"
	"strings"
	"testing"
)

// desktopApps is a real Linux desktop's catalogue (Omarchy, 2026-10-13), where the user said
// "open omawrite" (heard "Omerite", then "Oh my right") and "open neovim" (heard "the oven").
var desktopApps = func() []App {
	var out []App
	for _, n := range strings.Split("Aether|Avahi SSH Server Browser|Avahi VNC Server Browser|Avahi Zeroconf Browser|"+
		"Basecamp|btop++|Chromium|cliamp|Cyberpunk 2077|Deskflow|Discord|Disk Usage|Disks|Docker|Document Viewer|"+
		"Fcitx 5 Configuration|Files|Firefox|Foot|Foot Client|Foot Server|Google Contacts|Google Maps|Google Messages|"+
		"Google Photos|Hardware Locality lstopo|HEY|Image Viewer|Kdenlive|LibreOffice|LibreOffice Base|LibreOffice Calc|"+
		"LibreOffice Draw|LibreOffice Impress|LibreOffice Math|LibreOffice Writer|Limine-snapper-restore|LocalSend|"+
		"Manage Printing|Media Player|Moonlight|Neovim|OBS Studio|Obsidian|Omacalc|Omacut|Omawrite|Pinta|Print Settings|"+
		"Qt V4L2 test Utility|Qt V4L2 video capture utility|Red Dead Redemption|Steam|Sunshine|Tensaku|"+
		"The Witcher 3: Wild Hunt — Remastered|uuctl|WhatsApp|X|Xournal++|YouTube|Zoom", "|") {
		out = append(out, App{Name: n})
	}
	return out
}()

func TestFindMisheardNames(t *testing.T) {
	for heard, want := range map[string]string{
		"Omerite":     "Omawrite", // spelling: 2 edits
		"Oh my right": "Omawrite", // sound only
		"omah write":  "Omawrite",
		"NeoVim":      "Neovim", // exact, ignoring case
		"neo vim":     "Neovim",
		"Steam":       "Steam",
	} {
		p, err := Find(desktopApps, heard)
		if err != nil || p.App.Name != want {
			t.Errorf("%q -> %q, %v; want %q", heard, p.App.Name, err, want)
			continue
		}
		exact := strings.EqualFold(strings.ReplaceAll(heard, " ", ""), want)
		if p.Fuzzy == exact {
			t.Errorf("%q: fuzzy %v", heard, p.Fuzzy)
		}
		if p.Fuzzy && p.HeardAs != heard {
			t.Errorf("%q: heard_as %q", heard, p.HeardAs)
		}
	}
}

// "the oven" is one sound away from Neovim: never launched, only suggested.
func TestFindNeverLaunchesASoundNearGuess(t *testing.T) {
	_, err := Find(desktopApps, "the oven")
	var nf *NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("err = %v", err)
	}
	if len(nf.Suggestions) == 0 || nf.Suggestions[0] != "Neovim" {
		t.Fatalf("suggestions %v", nf.Suggestions)
	}
	for _, q := range []string{"the kitchen lights", "oven", "toaster", "foo"} {
		if p, err := Find(desktopApps, q); err == nil {
			t.Errorf("%q launched %q", q, p.App.Name)
		}
	}
}

func TestClosest(t *testing.T) {
	got := Closest(desktopApps, "Omerite", 3)
	if len(got) == 0 || got[0].Name != "Omawrite" || got[0].Score < 0.7 {
		t.Fatalf("closest %v", got)
	}
	if len(got) > 1 && got[1].Score >= got[0].Score {
		t.Fatalf("not ranked: %v", got)
	}
	if got := Closest(desktopApps, "steam", 3); got[0].Name != "Steam" || got[0].Score != 1 {
		t.Fatalf("exact %v", got)
	}
	if got := Closest(desktopApps, "zzzzqqq", 3); len(got) != 0 {
		t.Fatalf("nothing close: %v", got)
	}
}
