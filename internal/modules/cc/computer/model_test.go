package computer

import (
	"strings"
	"testing"
)

func TestFitOf(t *testing.T) {
	for _, c := range []struct {
		label, model string
		tags         []string
		want         Fit
	}{
		{"background", "gemma-4-26b-a4b", []string{"live", "vision", "large"}, FitLarge},
		{"background", "qwen3.5-9b", []string{"live", "vision"}, FitSmall},
		{"cloud", "anything", nil, FitLarge},
		{"live", "someone/their-31b-q4", nil, FitLarge},
		{"live", "their-7.5b:latest", nil, FitSmall},
		{"live", "a-model-with-4bit-weights", nil, FitUnknown},
		{"live", "", nil, FitUnknown},
	} {
		if got := fitOf(c.label, c.model, c.tags); got != c.want {
			t.Errorf("fitOf(%q, %q, %v) = %q, want %q", c.label, c.model, c.tags, got, c.want)
		}
	}
}

func TestFitNote(t *testing.T) {
	if n := fitNote(FitLarge, "gemma-4-26b-a4b"); n != "" {
		t.Errorf("a large model's note = %q, want none", n)
	}
	if n := fitNote(FitSmall, "qwen3.5-9b"); !strings.Contains(n, "qwen3.5-9b") || !strings.Contains(n, "small model") {
		t.Errorf("a small model's note = %q", n)
	}
	if n := fitNote(FitUnknown, "mystery"); !strings.Contains(n, "can't tell") {
		t.Errorf("an unknown model's note = %q", n)
	}
}
