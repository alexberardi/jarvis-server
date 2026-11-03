package textfilter

import "testing"

// The follow-through shapes (followthrough.go), from a desktop agent's real test pass.
func TestFollowThroughShapes(t *testing.T) {
	for _, s := range []string{
		"Got it, Jordan. Parking spot B12 noted.",
		"Noted!",
		"Got it — all saved.",
		"Parking spot stored.",
	} {
		if !ResponseClaimsAction(s) {
			t.Errorf("ResponseClaimsAction(%q) = false", s)
		}
	}
	for _, s := range []string{
		"That wasn't saved, sorry.",
		"It's not noted anywhere.",
		"As you noted earlier, it rains.",
		"Got it.",
	} {
		if ResponseClaimsAction(s) {
			t.Errorf("ResponseClaimsAction(%q) = true", s)
		}
	}
	for _, s := range []string{"I'll keep that in mind, Jordan.", "Got it, Jordan. Parking spot B12 noted.", "I've made a note of it.",
		"Got it, Jordan. Your parking spot is B12.", "Okay — your locker code is 4471.",
		// #435: an offer after the claim is no question back.
		"Got it, Jordan—your locker code is 4471. Need anything else?"} {
		if !ClaimsRemembered(s) {
			t.Errorf("ClaimsRemembered(%q) = false", s)
		}
	}
	for _, s := range []string{"I can't save that here.", "Do you mean the garage spot?", "Sorry, I couldn't save that.",
		"Got it — which locker is that?", "Sure. Which locker is that for?"} {
		if ClaimsRemembered(s) {
			t.Errorf("ClaimsRemembered(%q) = true", s)
		}
	}

	for _, s := range []string{
		"Remember that my parking spot is B12.",
		"remember my locker code is 4471",
		"Can you remember that my parking spot is B12?",
		"Jarvis, please remember that the spare key is under the mat",
		"don't forget that I parked on level 3",
		"note that the wifi password changed",
	} {
		if !IsRememberRequest(s) {
			t.Errorf("IsRememberRequest(%q) = false", s)
		}
	}
	for _, s := range []string{"Do you remember where my parking spot is?", "I can't remember", "what do you remember about me", "remember",
		"save this as a recipe"} {
		if IsRememberRequest(s) {
			t.Errorf("IsRememberRequest(%q) = true", s)
		}
	}

	for _, s := range []string{
		"Not in my memory, Jordan. Want me to jot that down for you?",
		"I don't have your gate code stored. Could you tell me what it is?",
		"I don't have access to your gate code.",
		"I don't have any record of that.",
		"I don't remember you telling me.",
		"Nothing on file for that.",
	} {
		if !DeniesStoredFact(s) {
			t.Errorf("DeniesStoredFact(%q) = false", s)
		}
	}
	for _, s := range []string{"Your parking spot is B12.", "It's 8:27 PM.", "I don't think it will rain."} {
		if DeniesStoredFact(s) {
			t.Errorf("DeniesStoredFact(%q) = true", s)
		}
	}
	for _, s := range []string{"Do you remember where my parking spot is?", "What's my gate code?", "where did I leave my keys?"} {
		if !AsksOwnFact(s) {
			t.Errorf("AsksOwnFact(%q) = false", s)
		}
	}
	for _, s := range []string{"What's the weather like?", "Remember that my spot is B12.", "what time is it?"} {
		if AsksOwnFact(s) {
			t.Errorf("AsksOwnFact(%q) = true", s)
		}
	}

	for _, s := range []string{
		"Open Omerite and type hello there.",
		"open omawrite and write hello there",
		"open omawrite then type hello there",
		"launch the terminal and then type git status",
		"open notes & type buy milk",
		"Open Oma Right and jot down a note to update Steam.",
		"Open Neovim and Typeello World.",
	} {
		if !IsTypeAfterOpen(s) {
			t.Errorf("IsTypeAfterOpen(%q) = false", s)
		}
	}
	for in, want := range map[string]string{
		"Open Omerite and type hello there.":                  "hello there",
		"Open Omerite and type hello there.\n/no_think":       "hello there",
		"Open Neovim and Typeello World.":                     "ello World",
		"Open Oma Right and jot down a note to update Steam.": "a note to update Steam",
	} {
		if got := TypeAfterOpenWords(in); got != want {
			t.Errorf("TypeAfterOpenWords(%q) = %q, want %q", in, got, want)
		}
	}
	for _, s := range []string{"open omawrite", "open omawrite and type", "type hello there", "open the typewriter app", "open vscode and typescript files", "open fonts and typefaces"} {
		if IsTypeAfterOpen(s) {
			t.Errorf("IsTypeAfterOpen(%q) = true", s)
		}
	}
}

// A polite memory request arms the force guard like a plain one; other questions still don't.
func TestForceToolsGateRememberRequest(t *testing.T) {
	pool := []string{"remember", "open"}
	if !ForceToolsGate("Can you remember that my parking spot is B12?", []string{"Sure, noted."}, pool) {
		t.Error("polite remember request did not arm the guard")
	}
	if !ForceToolsGate("Remember that my parking spot is B12.", []string{"Got it, Jordan. Parking spot B12 noted."}, pool) {
		t.Error("remember request did not arm the guard")
	}
	if ForceToolsGate("Can you open it?", []string{"Sure."}, pool) {
		t.Error("an ordinary question armed the guard")
	}
	if ForceToolsGate("Remember that my parking spot is B12.", []string{"Noted."}, []string{"open", "launch"}) {
		t.Error("no matching keyword must still disarm (the pool decides)")
	}
}

// #221: "stop watching the example.com one" got "I've stopped watching it" with no
// cancel_watch call, and the watch stayed on.
func TestStopWatchingShapes(t *testing.T) {
	for _, s := range []string{"stop watching the example.com one", "Stop tracking the kettle price.",
		"cancel the toaster watch", "please stop monitoring that page", "Jarvis, quit watching the kettle",
		"don't watch the kettle price anymore", "you can stop keeping an eye on the toaster",
		"remove the example.com watch", "I don't need the kettle watch anymore"} {
		if !IsStopWatchingRequest(s) {
			t.Errorf("IsStopWatchingRequest(%q) = false", s)
		}
	}
	for _, s := range []string{"what are you watching?", "watch the kettle price", "stop the music",
		"cancel my timer", "tell me when I leave home", "keep an eye on the toaster price"} {
		if IsStopWatchingRequest(s) {
			t.Errorf("IsStopWatchingRequest(%q) = true", s)
		}
	}
	for _, s := range []string{"I've stopped watching the example.com price.", "Done, I'm no longer watching it.",
		"Okay, I won't watch it anymore.", "Stopped watching price on example.com item.",
		"The example.com watch has been cancelled.", "I removed that watch.", "I'll stop watching the kettle price.", "Got it."} {
		if !ClaimsStoppedWatching(s) {
			t.Errorf("ClaimsStoppedWatching(%q) = false", s)
		}
	}
	for _, s := range []string{"Which one: the kettle price or the toaster price?",
		"I couldn't find a watch matching that. Nothing was cancelled.", "You aren't watching anything.",
		"I haven't stopped it yet."} {
		if ClaimsStoppedWatching(s) {
			t.Errorf("ClaimsStoppedWatching(%q) = true", s)
		}
	}
}

// #575: a reply whose last sentence promises a step it never took ended the turn.
func TestPromisesActionShapes(t *testing.T) {
	for _, s := range []string{"Let me open it in the browser instead…", "I'll run an errand to download your bank statement for September.",
		"wikipedia.org isn't an app here. Let me open it in the browser instead.", "Sure! I'll go ahead and search for that.",
		"I'm going to check the page now.", "Okay, let me try that again.", "I will look it up."} {
		if !PromisesAction(s) {
			t.Errorf("PromisesAction(%q) = false", s)
		}
	}
	for _, s := range []string{"Let me know if you need anything else.", "Let me know.", "Should I run an errand for that?",
		"Let me check. It's 72 degrees and sunny.", "I'll remind you at 5.", "I opened it in the browser.",
		"I can't open that, sorry.", "Want me to open it in the browser?", "I'll keep that in mind.", "",
		"Your watch is set. I'll check back every hour and ping you if the price drops.", "I'll open the garage when you get home.",
		"I'll get back to you when the research is finished.", "The errand is running. I'll read you the result when it's done.",
		"I'll check the price tomorrow.", "I'll look again at 5 pm.", "Once it's done, I'll read you the summary."} {
		if PromisesAction(s) {
			t.Errorf("PromisesAction(%q) = true", s)
		}
	}
}
