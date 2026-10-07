package cc

import (
	"context"
	"net/http"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Wiring for the voice pipeline (5b): routes, server tools, the conversation sweeper and
// traces.

// registerVoice mounts 5b's routes on the CC listener.
func (m *Module) registerVoice(mux *http.ServeMux) {
	const v0 = "/api/v0"
	mux.HandleFunc("POST "+v0+"/conversation/start", m.node(m.handleConversationStart))
	mux.HandleFunc("POST "+v0+"/conversation/end", m.node(m.handleConversationEnd))
	mux.HandleFunc("POST "+v0+"/voice/command", m.node(m.handleVoiceCommand))
	mux.HandleFunc("POST "+v0+"/voice/command/stream", m.node(m.handleVoiceStream))
	mux.HandleFunc("POST "+v0+"/voice/command/continue", m.node(m.handleContinue))
	mux.HandleFunc("POST "+v0+"/voice/command/continue/stream", m.node(m.handleContinueStream))
	mux.HandleFunc("POST "+v0+"/voice/acknowledge", m.node(m.handleAcknowledge))
	mux.HandleFunc("POST "+v0+"/wake-response", m.node(m.handleWakeResponse))

	// Node plugin API.
	mux.HandleFunc("POST "+v0+"/node/llm/chat", m.node(m.handleNodeLLMChat))
	mux.HandleFunc("GET "+v0+"/generate/date-context", m.node(m.handleDateContext))
	mux.HandleFunc("POST "+v0+"/node/push-notification", m.node(m.handleNodePush))
	mux.HandleFunc("POST "+v0+"/node/inbox-item", m.node(m.handleNodeInboxItem))
	mux.HandleFunc("POST "+v0+"/node/send-link", m.node(m.handleNodeSendLink))

	// Media proxy and the node-mic enrollment handoff.
	mux.HandleFunc("POST "+v0+"/media/tts/speak", m.node(m.handleTTSSpeak))
	mux.HandleFunc("POST "+v0+"/media/tts/speak/stream", m.node(m.handleTTSSpeakStream))
	mux.HandleFunc("POST "+v0+"/media/whisper/transcribe", m.node(m.handleTranscribe))
	mux.HandleFunc("POST "+v0+"/media/whisper/voice-profiles/enroll", m.node(m.handleEnroll))
	mux.HandleFunc("POST "+v0+"/media/whisper/voice-profiles/verify", m.node(m.handleVerify))
	mux.HandleFunc("POST "+v0+"/mobile/voice-profile/start-node-enrollment", m.user(m.handleStartNodeVoice("enroll_voice", 8.0)))
	mux.HandleFunc("POST "+v0+"/mobile/voice-profile/start-node-verification", m.user(m.handleStartNodeVoice("verify_voice", 5.0)))
	mux.HandleFunc("GET "+v0+"/mobile/voice-profile-results/{request_id}", m.user(m.handlePollVoiceProfileResult))
}

// registerServerTools registers the server tools whose dependencies exist in this phase.
// 5c registers its own (remember/recall/forget, run_errand/schedule_errand/
// list_scheduled_errands, make_phone_call, control_device) through Module.ServerTools() when
// their modules land. answer_question and get_command_examples stay unregistered: both are
// disabled in legacy, and the node's own answer_question client tool would otherwise be
// shadowed.
func (m *Module) registerServerTools() {
	m.tools.Register(servertools.NewRequestValidation())
	m.tools.Register(servertools.NewIdentifySpeaker())
	m.tools.Register(servertools.NewHAEntities())
	m.tools.Register(m.phoneService().Tool()) // make_phone_call (5c, phone_wire.go)
	m.registerErrandTools()
	gate := func(ctx context.Context, hh string) bool {
		return m.settings.Bool(ctx, settingWebSearch, settings.Scope{HouseholdID: hh})
	}
	search := m.WebSearch
	if search == nil {
		search = &servertools.DuckDuckGo{}
	}
	m.tools.Register(servertools.NewQuickSearch(search, nil, gate, m.deps.Log))
	if m.deps.Queue != nil {
		m.tools.Register(servertools.NewDeepResearch(m.deps.Queue, gate, m.deps.Log))
		if m.LLM != nil && m.Notify != nil {
			servertools.NewResearchRunner(search, nil, m.LLM, m.Notify, m.deps.Log).Register(m.deps.Queue)
		}
	}
}

// ServerTools is the registry 5c registers its tools in (valid after Register).
func (m *Module) ServerTools() *servertools.Registry { return m.tools }

// recordVoiceTrace stores a request trace for a voice route (latency_logger), off the
// response path.
func (m *Module) recordVoiceTrace(n *nodeCtx, convID, kind, command, answer string, start time.Time, err error) {
	t := Trace{ConversationID: convID, RequestType: kind, Source: "node", NodeID: n.ID, HouseholdID: n.HouseholdID,
		UserCommand: command, AssistantMessage: answer,
		TotalDurationMS: float64(m.now().Sub(start).Microseconds()) / 1000}
	if err != nil {
		t.Status, t.ErrorMessage = "error", err.Error()
	}
	m.recordTraceAsync(t)
}

func (m *Module) recordTraceAsync(t Trace) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := m.RecordTrace(ctx, t); err != nil {
			m.deps.Log.Debug("cc: trace write failed", "err", err)
		}
	}()
}

func scopeHN(hh, node string) settings.Scope { return settings.Scope{HouseholdID: hh, NodeID: node} }
