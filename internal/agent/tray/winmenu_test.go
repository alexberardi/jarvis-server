package tray

import (
	"image"
	"image/color"
	"testing"
)

func TestWinMenuFlags(t *testing.T) {
	items := Items(State{Status: "Running", OpenURL: "http://x/", VoiceAvailable: true, Voice: true})
	want := map[int32]uint32{IDHeader: mfGrayed, idSep1: mfSeparator, IDVoice: mfChecked, IDOpen: mfString, idSep2: mfSeparator, IDQuit: mfString}
	for _, it := range items {
		if got := winMenuFlags(it); got != want[it.ID] {
			t.Errorf("item %d %q: flags %#x, want %#x", it.ID, it.Label, got, want[it.ID])
		}
	}
	if winMenuFlags(Item{ID: IDVoice, Enabled: true, Toggle: true}) != mfString {
		t.Error("an unchecked toggle got a checkmark")
	}
}

func TestWinTip(t *testing.T) {
	tip := winTip(Header(State{}))
	if string(rune(tip[0])) != "J" || tip[len("Jarvis Automation Agent — Running")] != 0 {
		t.Fatalf("tip %v", tip[:40])
	}
	long := winTip(string(make([]rune, 300)) + "x")
	if long[127] != 0 {
		t.Fatal("tip not NUL-terminated")
	}
}

func TestWinIconBits(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	img.SetNRGBA(0, 0, color.NRGBA{R: 1, G: 2, B: 3, A: 4})
	img.SetNRGBA(1, 0, color.NRGBA{R: 5, G: 6, B: 7, A: 255})
	got := winIconBits(img)
	want := []byte{3, 2, 1, 4, 7, 6, 5, 255}
	if string(got) != string(want) {
		t.Fatalf("bits %v, want %v", got, want)
	}
}
