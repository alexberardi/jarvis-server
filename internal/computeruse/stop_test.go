package computeruse

import "testing"

func TestIsStopPhrase(t *testing.T) {
	for s, want := range map[string]bool{
		"stop": true, "Stop.": true, "Jarvis, stop!": true, "stop it": true, "Stop that.": true,
		"cancel": true, "Cancel that, please.": true, "That's enough.": true, "That’s enough": true,
		"abort": true, "quit": true, "Never mind.": true, "nevermind": true, "OK stop now": true,

		"don't stop": false, "keep going": false, "stop the music": false, "wait": false,
		"no": false, "hold on": false, "what's the weather": false, "": false,
		// A recording's words don't stop a task Jarvis is doing (#288's review).
		"That's all.": false, "That's all, thanks.": false, "Done.": false, "I'm done.": false,
		"Stop recording.": false, "That's it.": false,
	} {
		if got := IsStopPhrase(s); got != want {
			t.Errorf("IsStopPhrase(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestIsRecordingStopPhrase(t *testing.T) {
	for s, want := range map[string]bool{
		"stop": true, "Stop.": true, "Jarvis, stop!": true, "Cancel that, please.": true, "Never mind.": true,
		// #288: the recording's own words, as whisper writes them.
		"Stop recording.": true, "stop the recording": true, "Stop watching.": true, "I'm done.": true,
		"I’m done": true, "Im done": true, "I am done.": true, "Hey Jarvis, I'm done.": true, "Okay, I'm done.": true,
		"I'm finished.": true, "All done.": true, "Done.": true, "That's it.": true, "That’s it, thanks.": true,
		"Alright, that's it.": true, "Jarvis, stop recording now.": true, "We're done.": true,

		"don't stop": false, "keep going": false, "stop the music": false, "wait": false,
		"no": false, "hold on": false, "what's the weather": false, "": false,
		"I'm done with dinner": false, "I'm done with the dishes.": false, "Is it done?": false,
		"done yet?": false, "that's it for the lights": false, "stop recording my calls": false,
	} {
		if got := IsRecordingStopPhrase(s); got != want {
			t.Errorf("IsRecordingStopPhrase(%q) = %v, want %v", s, got, want)
		}
	}
}
