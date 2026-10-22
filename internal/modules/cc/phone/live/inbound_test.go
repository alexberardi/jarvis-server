package live

import (
	"reflect"
	"strings"
	"testing"
)

// feedAll streams s through p a few bytes at a time, as the model does.
func feedAll(p *TokenParser, s string) (string, []ToolEvent) {
	var text strings.Builder
	var events []ToolEvent
	for i := 0; i < len(s); i += 3 {
		t, evs := p.Feed(s[i:min(i+3, len(s))])
		text.WriteString(t)
		events = append(events, evs...)
	}
	text.WriteString(p.Flush())
	return text.String(), events
}

func TestInboundToolsAreExactlyTakeMessageAndHangUp(t *testing.T) {
	// The receptionist's whole tool set. Adding one (transfer, a household lookup) is a change
	// to what an untrusted caller can reach: it must change this test, and get a review.
	if want := []string{"take_message", "hang_up"}; !reflect.DeepEqual(InboundTools, want) {
		t.Fatalf("InboundTools = %q, want %q", InboundTools, want)
	}
	prompt := BuildInboundSystemPrompt(InboundBrief{Household: "The Averys"})
	for _, tok := range []string{"[TAKE_MESSAGE:", "[HANGUP]"} {
		if !strings.Contains(prompt, tok) {
			t.Errorf("the prompt does not offer %s", tok)
		}
	}
	for _, tok := range []string{"[ESCALATE", "[OUTCOME", "[DTMF", "[TRANSFER"} {
		if strings.Contains(prompt, tok) {
			t.Errorf("the prompt offers %s, which the receptionist does not have", tok)
		}
	}
}

func TestInboundParserActsOnlyOnItsTwoTools(t *testing.T) {
	text, evs := feedAll(NewInboundParser(),
		"Thanks Bob, I'll pass that on. [ESCALATE: what is the wifi password?][TRANSFER: 911][CALL: +12125550100]"+
			"[TAKE_MESSAGE: Bob Jones | Saturday's game | call him back at 555-0142] Goodbye! [HANGUP]")
	want := []ToolEvent{Refused{"ESCALATE"}, Refused{"TRANSFER"}, Refused{"CALL"},
		TakeMessage{"Bob Jones", "Saturday's game", "call him back at 555-0142"}, Hangup{}}
	if !reflect.DeepEqual(evs, want) {
		t.Fatalf("events %#v, want %#v", evs, want)
	}
	if strings.Contains(text, "[") || strings.Contains(text, "wifi") || strings.Contains(text, "911") {
		t.Fatalf("a refused command was spoken: %q", text)
	}
	if text != "Thanks Bob, I'll pass that on.  Goodbye! " {
		t.Fatalf("spoken %q", text)
	}
}

func TestInboundParserLeavesOrdinaryBracketsAlone(t *testing.T) {
	text, evs := feedAll(NewInboundParser(), "Sure [laughs], a moment [A note] then.")
	if len(evs) != 0 || text != "Sure [laughs], a moment [A note] then." {
		t.Fatalf("text %q, events %v", text, evs)
	}
}

func TestTakeMessageFields(t *testing.T) {
	for _, tc := range []struct {
		body string
		want TakeMessage
	}{
		{"TAKE_MESSAGE: unknown | a parcel | it's at the back door", TakeMessage{"", "a parcel", "it's at the back door"}},
		{"TAKE_MESSAGE: Ann|dentist", TakeMessage{"Ann", "dentist", ""}},
		{"TAKE_MESSAGE: call me back about the car", TakeMessage{Message: "call me back about the car"}},
	} {
		if got := parseInbound(tc.body); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("parseInbound(%q) = %#v, want %#v", tc.body, got, tc.want)
		}
	}
}

func TestOutboundParserIsUnchanged(t *testing.T) {
	// The zero TokenParser still reads the call brain's tokens and speaks unknown ones.
	var p TokenParser
	text, evs := feedAll(&p, "Done. [OUTCOME: booked 7pm][TAKE_MESSAGE: x] Bye [HANGUP]")
	if !reflect.DeepEqual(evs, []ToolEvent{Outcome{"booked 7pm"}, Hangup{}}) || !strings.Contains(text, "[TAKE_MESSAGE: x]") {
		t.Fatalf("text %q events %#v", text, evs)
	}
}

func TestInboundDisclosureAndPromptCarryOnlyTheHouseholdName(t *testing.T) {
	b := InboundBrief{Household: "  The   Averys "}
	d := BuildInboundDisclosure(b)
	for _, want := range []string{"The Averys", "AI assistant", "recorded", "Who's calling"} {
		if !strings.Contains(d, want) {
			t.Errorf("disclosure %q lacks %q", d, want)
		}
	}
	msgs := InboundInitialMessages(b)
	if len(msgs) != 2 || msgs[0][0] != "system" || msgs[1] != [2]string{"assistant", d} {
		t.Fatalf("initial messages %q", msgs)
	}
	if got := BuildInboundDisclosure(InboundBrief{}); !strings.Contains(got, "the household") {
		t.Errorf("no name: %q", got)
	}
}
