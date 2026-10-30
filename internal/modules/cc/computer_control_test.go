package cc

import (
	"context"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// computerTools is a desktop agent's tool list with its computer-control tools.
const computerTools = `[{"type":"function","function":{"name":"desktop_open","description":"Open an app.","parameters":{"type":"object","properties":{"target":{"type":"string"}},"required":["target"]}}},` +
	`{"type":"function","function":{"name":"computer_act","description":"Act on the computer.","parameters":{"type":"object","properties":{}}}}]`

// setComputerControl makes the env's node a desktop agent reporting the switch: "on", "off", or
// "" for an agent that never reported it (an older one).
func (ve *voiceEnv) setComputerControl(state string) {
	ve.t.Helper()
	ve.makeDesktop(0)
	caps := `{"browser_extension":"connected"}`
	if state != "" {
		caps = `{"browser_extension":"connected","computer_control":"` + state + `"}`
	}
	if _, err := ve.d.Write.Exec(`UPDATE cc_nodes SET capabilities = ? WHERE node_id = ?`, caps, ve.node.id); err != nil {
		ve.t.Fatal(err)
	}
}

func offeredNames(conv *conversation) map[string]bool {
	out := map[string]bool{}
	for _, t := range conv.tools {
		out[toolName(t)] = true
	}
	return out
}

// C11: computer tools are offered only for a computer whose switch is on; the agent's other
// tools are offered either way.
func TestComputerToolsOnlyWithTheSwitchOn(t *testing.T) {
	for _, c := range []struct {
		name, state string
		desktop     bool
		want        bool
	}{
		{"switch on", "on", true, true},
		{"switch off", "off", true, false},
		{"never reported (an older agent)", "", true, false},
		{"not a computer", "", false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			ve := newVoiceEnv(t, prompts.Qwen3_8B)
			if c.desktop {
				ve.setComputerControl(c.state)
			}
			ve.start("c1", computerTools)
			got := offeredNames(ve.m.convs.get("c1"))
			if got["computer_act"] != c.want {
				t.Fatalf("computer_act offered %v, want %v", got["computer_act"], c.want)
			}
			if !got["desktop_open"] {
				t.Fatal("the agent's other tools went with it")
			}
		})
	}
}

// The switch and recorder tools are jarvisd's to call (enable_computer_control, start_recording):
// the live model never gets them, even at a computer whose switch is on.
func TestSwitchAndRecorderToolsNeverOffered(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	ve.setComputerControl("on")
	tools := strings.TrimSuffix(computerTools, "]") +
		`,{"type":"function","function":{"name":"computer_switch","description":"Turn it on or off.","parameters":{"type":"object","properties":{}}}}` +
		`,{"type":"function","function":{"name":"computer_record_start","description":"Start recording.","parameters":{"type":"object","properties":{}}}}]`
	ve.start("c1", tools)
	got := offeredNames(ve.m.convs.get("c1"))
	if !got["computer_act"] || got["computer_switch"] || got["computer_record_start"] {
		t.Fatalf("offered %v", got)
	}
}

func TestComputerTargets(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	ctx := context.Background()
	if cc, err := ve.m.computerControl(ctx, voiceHH); err != nil || len(cc.on) != 0 || len(cc.off) != 0 {
		t.Fatalf("no computers: %+v %v", cc, err)
	}
	ve.setComputerControl("off")
	cc, err := ve.m.computerControl(ctx, voiceHH)
	if err != nil || len(cc.on) != 0 || len(cc.off) != 1 || cc.off[0].id != ve.node.id {
		t.Fatalf("off: %+v %v", cc, err)
	}
	ve.setComputerControl("on")
	cc, _ = ve.m.computerControl(ctx, voiceHH)
	if got := cc.targets(); len(got) != 1 || got[0] != ve.node.id {
		t.Fatalf("on: targets %v", got)
	}
	if cc, _ := ve.m.computerControl(ctx, "another-household"); len(cc.on)+len(cc.off) != 0 {
		t.Fatalf("another household's computers: %+v", cc)
	}
}

func TestComputerControlOffMessage(t *testing.T) {
	for _, c := range []struct {
		cc   computerControl
		want string
	}{
		{computerControl{off: []computerNode{{id: "n1", name: "Office-PC"}}},
			`Computer control is off. Turn it on with "Computer control" in the Jarvis Automation menu on Office-PC.`},
		{computerControl{off: []computerNode{{id: "n1", name: "Office-PC"}, {id: "n2", name: "Laptop"}}},
			`Computer control is off. Turn it on with "Computer control" in the Jarvis Automation menu on Office-PC or Laptop.`},
		{computerControl{off: []computerNode{{id: "n1"}}},
			`Computer control is off. Turn it on with "Computer control" in the Jarvis Automation menu on your computer.`},
		{computerControl{},
			`Computer control needs the Jarvis Automation agent on a computer: set it up there, then turn on "Computer control" in its menu.`},
	} {
		if got := c.cc.offMessage(); got != c.want {
			t.Errorf("%+v:\n got %s\nwant %s", c.cc, got, c.want)
		}
	}
}

func TestNeedsComputer(t *testing.T) {
	for s, want := range map[string]bool{
		"order me a phone case":                                       true,
		"Order a phone case":                                          true,
		"buy a new phone case on amazon":                              true,
		"can you purchase the blue mug":                               true,
		"check out my cart":                                           true,
		"add the charger to my cart":                                  true,
		"fill out the passport renewal form":                          true,
		"what's the weather":                                          false,
		"put the movies in order":                                     false,
		"what did I order last week":                                  false,
		"call Joe's pizza and order a large pepperoni":                false,
		"remind me to buy milk tomorrow":                              false,
		"add buy batteries to my shopping list":                       false,
		"research the best smart watch":                               false,
		"open steam":                                                  false,
		"Open Neovim and Typeello World.":                             false, // desktop tools, not an errand
		"download my card statement for september":                    true,
		"do my budget: download the bank statements":                  true,
		"fill in the plot application at http://127.0.0.1:8080/form/": true,
		"fill in the blanks for me":                                   false,
		// Typed words are the text, not the request (#435).
		"type buy more coffee filters into omawrite":       false,
		"write order the cake on friday in slack":          false,
		"Hey Jarvis, type check out the new deck in notes": false,
		"buy a type-c cable":                               true,
		"find the right type of phone case and order it":   true,
		// #575: asking for the computer outright.
		"use the computer to find today's featured article on wikipedia.org and tell me what its lead image shows": true,
		"Use my laptop to look up the train times": true,
		"how do I use the remote":                  false,
		"open wikipedia.org":                       false,
		// C27: documents and spreadsheets are errands (write_document), "write" included.
		"Deep research the Apollo 11 moon landing conspiracy theories and put a summary of them in a Word document called hoax-2.docx in my Documents.": true,
		"Write a 2 page article about Apollo 11 and save it as apollo.odt":                                                                              true,
		"add a row for internet to my budget spreadsheet":                                                                                               true,
		"type hello world into the document":                                                                                                            false,
		"remind me to update the spreadsheet":                                                                                                           false,
	} {
		if got := needsComputer(s); got != want {
			t.Errorf("needsComputer(%q) = %v, want %v", s, got, want)
		}
	}
}

// With the switch off on every computer, a request that needs one gets the hint saying so, and
// run_errand refuses it with where to turn it on; with one on, the hint says to use run_errand.
func TestComputerControlOffHintAndErrandRefusal(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	ve.setComputerControl("off")
	ve.start("c1", weatherTool)
	ve.eng.say("Computer control is off.")
	ve.turn("/api/v0/voice/command", "c1", "order me a phone case", map[string]any{"turn_source": "wake", "wake_confidence": 0.9}).want(200)
	u := lastUserOf(ve)
	if !strings.Contains(u, "[computer control: off on every computer.") || !strings.Contains(u, `"Computer control" in the Jarvis Automation menu`) {
		t.Fatalf("no hint: %q", u)
	}
	errand := func(goal string) string {
		res := ve.m.tools.Execute(context.Background(), servertools.Call{Name: "run_errand", Args: servertools.Obj("goal", goal)},
			servertools.Turn{ConversationID: "c1", HouseholdID: voiceHH, NodeID: ve.node.id})
		return pyjson.Dumps(res, false)
	}
	if got := errand("Order a phone case"); !strings.Contains(got, `"error": "computer_control_off"`) || !strings.Contains(got, "Jarvis Automation menu") {
		t.Fatalf("run_errand with the switch off: %s", got)
	}
	// An errand that needs no computer goes on as before (here: no queue in this env).
	if got := errand("research the best smart watch"); strings.Contains(got, "computer_control_off") {
		t.Fatalf("research refused: %s", got)
	}

	ve.eng.say("It's sunny.")
	ve.turn("/api/v0/voice/command", "c1", "what's the weather", map[string]any{"turn_source": "wake", "wake_confidence": 0.9}).want(200)
	if u := lastUserOf(ve); strings.Contains(u, "[computer control:") {
		t.Fatalf("hint on a turn that needs no computer: %q", u)
	}

	ve.setComputerControl("on")
	ve.eng.say("On it.")
	ve.turn("/api/v0/voice/command", "c1", "order me a phone case", map[string]any{"turn_source": "wake", "wake_confidence": 0.9}).want(200)
	if u := lastUserOf(ve); !strings.Contains(u, "[computer control: on. Do this with run_errand") {
		t.Fatalf("no run_errand hint with the switch on: %q", u)
	}
	if got := errand("Order a phone case"); strings.Contains(got, "computer_control_off") {
		t.Fatalf("run_errand with the switch on: %s", got)
	}
}

// An errand a phone call can do is not refused: with phone calls on, the errand planner may
// run "order a pie from Tony's" by phone (make_phone_call), so no switch is needed for it.
func TestComputerControlOffLeavesPhoneErrands(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	ve.setComputerControl("off")
	ve.start("c1", weatherTool)
	errand := func(goal string) string {
		res := ve.m.tools.Execute(context.Background(), servertools.Call{Name: "run_errand", Args: servertools.Obj("goal", goal)},
			servertools.Turn{ConversationID: "c1", HouseholdID: voiceHH, NodeID: ve.node.id})
		return pyjson.Dumps(res, false)
	}
	if got := errand("order me a phone case"); !strings.Contains(got, `"error": "computer_control_off"`) {
		t.Fatalf("phone calls off: %s", got)
	}

	ve.set(phone.SettingEnabled, true, settings.Scope{HouseholdID: voiceHH})
	if got := errand("order a large pie from Tony's for pickup at 6"); strings.Contains(got, "computer_control_off") {
		t.Fatalf("phone errand refused: %s", got)
	}
	ve.eng.say("On it.")
	ve.turn("/api/v0/voice/command", "c1", "order a large pie from Tony's for pickup at 6", map[string]any{"turn_source": "wake", "wake_confidence": 0.9}).want(200)
	if u := lastUserOf(ve); strings.Contains(u, "[computer control:") {
		t.Fatalf("hint with phone calls on: %q", u)
	}
}
