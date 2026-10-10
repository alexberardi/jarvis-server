package device

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/audio"
)

func writeWAV(t *testing.T, s []float32, rate int) string {
	t.Helper()
	var b bytes.Buffer
	if err := audio.WriteWAV(&b, s, rate); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "in.wav")
	if err := os.WriteFile(p, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestWAVCaptureServesFileThenSilence(t *testing.T) {
	p := writeWAV(t, []float32{0.5, -0.5, 0.25}, 16000)
	c, err := OpenCapture(WAVPrefix+p, 16000, 2)
	if err != nil {
		t.Fatal(err)
	}
	c.(*WAVCapture).paced = false
	buf := make([]int16, 8) // 4 stereo frames
	if err := c.Read(buf); err != nil {
		t.Fatal(err)
	}
	want := []int16{16384, 16384, -16384, -16384, 8192, 8192, 0, 0} // round(x × 32767), / 32768, × 32768

	if !slices.Equal(buf, want) {
		t.Fatalf("got %v want %v", buf, want)
	}
	if !c.(*WAVCapture).Done() {
		t.Fatal("Done should be true after the file")
	}
	if err := c.Read(make([]int16, 3)); err == nil {
		t.Fatal("a partial frame must be refused")
	}
}

func TestWAVCaptureResamplesAndPaces(t *testing.T) {
	p := writeWAV(t, make([]float32, 1600), 16000) // 0.1 s
	c, err := OpenWAVCapture(p, 48000, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(c.samples); n != 4800 {
		t.Fatalf("resampled to %d samples, want 4800", n)
	}
	start := time.Now()
	buf := make([]int16, 2400) // 50 ms
	for range 3 {
		if err := c.Read(buf); err != nil {
			t.Fatal(err)
		}
	}
	if el := time.Since(start); el < 140*time.Millisecond {
		t.Fatalf("3 × 50 ms paced reads took %v", el)
	}
}

func TestWAVPlaybackAppendsUtterances(t *testing.T) {
	p := filepath.Join(t.TempDir(), "out.wav")
	for _, chunk := range [][]int16{{1, 2, 3}, {4, 5}} {
		pb, err := OpenPlayback(WAVPrefix+p, 24000, 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := pb.Write(chunk); err != nil {
			t.Fatal(err)
		}
		if err := pb.Drain(); err != nil {
			t.Fatal(err)
		}
		if err := pb.Close(); err != nil {
			t.Fatal(err)
		}
	}
	f, _ := os.Open(p)
	defer f.Close()
	got, rate, ch, err := readPCM(f)
	if err != nil || rate != 24000 || ch != 1 || !bytes.Equal(got, pcmBytes([]int16{1, 2, 3, 4, 5})) {
		t.Fatalf("%v %d %d %v", got, rate, ch, err)
	}
	pb, _ := OpenPlayback(WAVPrefix+p, 16000, 1)
	_ = pb.Write([]int16{1})
	if err := pb.Close(); err == nil || !strings.Contains(err.Error(), "can't append") {
		t.Fatalf("appending another format: %v", err)
	}
}

func TestNativeBackendName(t *testing.T) {
	want := map[string]string{"linux": "alsa", "darwin": "audioqueue"}[runtime.GOOS]
	if Backend() != want {
		t.Fatalf("Backend() = %q, want %q", Backend(), want)
	}
	if want == "" {
		if _, err := OpenCapture("default", 48000, 1); err != ErrUnsupported {
			t.Fatalf("OpenCapture: %v, want ErrUnsupported", err)
		}
		if _, err := OpenPlayback("", 24000, 1); err != ErrUnsupported {
			t.Fatalf("OpenPlayback: %v, want ErrUnsupported", err)
		}
	}
}
