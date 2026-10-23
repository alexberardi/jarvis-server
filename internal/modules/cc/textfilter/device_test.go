package textfilter

import "testing"

func TestIsDeviceChangeRequest(t *testing.T) {
	yes := []string{
		"Turn off the office light bar",
		"turn off the office light bars",
		"Turn on the kitchen lights",
		"turn the office light bars off",
		"switch the fan on",
		"Power off the TV",
		"shut off the bedroom lights",
		"Can you turn off the office light bar?",
		"please turn on the living room lamp",
		"lock the front door",
		"Unlock the back door",
		"dim the bedroom lights",
	}
	no := []string{
		"",
		"Turn down the volume",
		"turn the volume up",
		"turn off the music",
		"Turn it off",
		"turn that off",
		"shut up",
		"mute",
		"set a timer for ten minutes",
		"what's the weather",
		"turn off the sound",
	}
	for _, s := range yes {
		if !IsDeviceChangeRequest(s) {
			t.Errorf("IsDeviceChangeRequest(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if IsDeviceChangeRequest(s) {
			t.Errorf("IsDeviceChangeRequest(%q) = true, want false", s)
		}
	}
}

func TestClaimsDeviceChanged(t *testing.T) {
	yes := []string{
		"Got it, Jordan. The office light bars are now off.",
		"The office light bars are off.",
		"Done — I've turned off the office light bars.",
		"I turned the TV on.",
		"The kitchen lights are on. Anything else?",
		"Front door's locked. I've locked the front door.",
		"Switched the fan off.",
	}
	no := []string{
		"",
		"Which light do you mean?",
		"I couldn't turn off the office light bars.",
		"The light bars didn't turn off — the node didn't respond.",
		"I'll turn off the office light bars.",
		"I wasn't able to reach the office light bars.",
		"The office light bars are already off.",
		"I can't control that device from here.",
		"Nothing was turned off.",
	}
	for _, s := range yes {
		if !ClaimsDeviceChanged(s) {
			t.Errorf("ClaimsDeviceChanged(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if ClaimsDeviceChanged(s) {
			t.Errorf("ClaimsDeviceChanged(%q) = true, want false", s)
		}
	}
}
