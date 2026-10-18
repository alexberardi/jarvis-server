// Command voicelane is the E2E voice lane's case runner (scripts/e2e voice, docs/dev/e2e.md):
// the real node software, `jarvis-agent listen --profile pi` (the build a Pi runs), hears each
// case's audio, and its spoken reply is recorded, transcribed and checked.
//
//	voicelane --sandbox N --agent BIN --out DIR [--wake-models DIR] [--cases FILE] [--filter RE]
//	          [--repeat 3] [--audio wav|pipewire] [--reply-wait 60s] [--silent-wait 5s] [--agent-arg FLAG]...
//
// A case's audio is the sandbox's own TTS (POST /api/v0/media/tts/speak as the seeded node)
// plus silence and generated noise. The reply is transcribed by the sandbox's whisper, and the
// case asserts the turn's outcome, the reply (a regexp), the tool the model called (the new
// lines of the sandbox's llm.jsonl, or a client tool the node ran) and the response time. A
// silent case asserts that nothing happened: no turn, no reply audio, no model call.
//
// --audio picks how the node gets its audio:
//   - wav (default): one `listen --once` per case with --voice-input wav:IN and --voice-output
//     wav:OUT. No audio devices. The reply is written, not played in real time, so barge-in
//     can't happen: those cases ("needs": "pipewire") are skipped. The response time is the
//     turn's time less the recording (speech-to-text, the model, TTS).
//   - pipewire: one long `listen` on the default devices, which scripts/e2e voice points at
//     its private PipeWire (JARVIS_E2E_AUDIO=private); each case is played into the virtual mic
//     (pw-play) while the virtual speaker's monitor is recorded (pw-record). The response time
//     is the end of the audio to the reply's first sound.
//
// The node runs with its own HOME and XDG dirs under --out, with the sandbox's seeded node
// identity, so the user's own agent is never touched. Writes --out/results.json in the E2E
// report's test shape (one test per case, with its runs), and per run
// cases/<case>/run-N/{utterance.wav,reply.wav,agent.log,run.json}. Exits 1 when a case failed.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	err := run(ctx, os.Args[1:], os.Stdout)
	var fail errFailed
	switch {
	case errors.As(err, &fail):
		os.Exit(1)
	case err != nil:
		fmt.Fprintln(os.Stderr, "voicelane:", err)
		os.Exit(2)
	}
}

// errFailed is a run with failed cases (exit 1, already reported).
type errFailed struct{ n int }

func (e errFailed) Error() string { return fmt.Sprintf("%d failed", e.n) }
