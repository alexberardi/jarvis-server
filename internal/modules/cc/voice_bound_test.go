package cc

import (
	"context"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/stt"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// A node's transcription never queues unbounded whisper or speaker-model work behind it: once
// wakeVerifyLimit verifications are running, the next one is dropped (fail open, counted) and
// its turn doesn't wait out the verdict wait.
func TestWakeVerificationIsDroppedWhenBusy(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	ve.set(settingWakeMode, "enforce", settings.Scope{HouseholdID: voiceHH})
	for range cap(ve.m.wakeSem) {
		ve.m.wakeSem <- struct{}{} // every slot busy
	}
	ve.start("c1", "[]")
	ve.transcribe("c1", true)
	if n := ve.m.wakeDrops.Load(); n != 1 {
		t.Fatalf("wake drops %d, want 1", n)
	}
	ve.stt.mu.Lock()
	calls := len(ve.stt.opts)
	ve.stt.mu.Unlock()
	if calls != 1 {
		t.Fatalf("%d Transcribe calls, want only the command's", calls)
	}
	start := time.Now()
	if v := ve.m.signals.wakeVerdict(context.Background(), "c1", 5*time.Second); v != nil || time.Since(start) > time.Second {
		t.Fatalf("a dropped verification gives no verdict at once: %v after %v", v, time.Since(start))
	}

	// A free slot verifies again, and gives its slot back.
	<-ve.m.wakeSem
	ve.start("c2", "[]")
	ve.transcribe("c2", true)
	ve.awaitWake("c2")
	if v := ve.m.signals.wakeVerdict(context.Background(), "c2", time.Second); v == nil {
		t.Fatal("a free slot ran no verification")
	}
	waitFor(t, func() bool { return len(ve.m.wakeSem) == cap(ve.m.wakeSem)-1 })
}

func TestVoiceCheckIsDroppedWhenBusy(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	for range cap(ve.m.voiceSem) {
		ve.m.voiceSem <- struct{}{}
	}
	ve.start("c1", "[]")
	ve.stt.voices = []stt.Voice{voiceAt(1, 0)}
	done := make(chan struct{})
	go func() { defer close(done); ve.transcribe("c1", true) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a dropped voice check blocked the transcription")
	}
	if n := ve.m.voiceDrops.Load(); n != 1 || len(ve.stt.voiceClips) != 0 {
		t.Fatalf("voice drops %d, clips %d; want 1 and 0", n, len(ve.stt.voiceClips))
	}
	// The dropped check didn't use up the conversation's reference: the next one takes it.
	<-ve.m.voiceSem
	ve.transcribe("c1", true)
	if len(ve.stt.voiceClips) != 1 || ve.m.signals.claimReference("c1") {
		t.Fatalf("the next check after a drop must take the reference (%d clips)", len(ve.stt.voiceClips))
	}
	if len(ve.m.voiceSem) != cap(ve.m.voiceSem)-1 {
		t.Fatal("the voice check kept its slot")
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal("condition never held")
}
