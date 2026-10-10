package cc

import (
	"errors"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/llm"
)

// A turn whose live model is refused for a lost GPU reports the sentence the user should hear
// (voice speaks it, chat shows it), not the raw 503.
func TestLLMErrTextGPUUnavailable(t *testing.T) {
	msg := "The GPU driver was updated; reboot the server to finish (Jarvis can't use the GPU until then)."
	gpu := &llm.APIError{Status: 503, Type: "model_not_loaded", Code: "model_not_loaded", Message: msg,
		Extra: map[string]any{"slot": "live", "state": map[string]any{"status": llm.StateGPUUnavailable, "error": msg}}}
	if got := llmErrText(gpu); got != msg {
		t.Fatalf("got %q", got)
	}
	other := errors.New("503 model_not_loaded: live model is failed: exit 1")
	if got := llmErrText(other); got != other.Error() {
		t.Fatalf("other errors unchanged: %q", got)
	}
}
