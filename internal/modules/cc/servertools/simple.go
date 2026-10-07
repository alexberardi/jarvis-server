package servertools

import (
	"context"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// request_validation (core/tools/request_validation_tool.py): returns the validation marker
// the engine turns into a validation_required stop.

// RequestValidation is the request_validation tool.
type RequestValidation struct{}

// NewRequestValidation returns the tool.
func NewRequestValidation() *RequestValidation { return &RequestValidation{} }

func (*RequestValidation) Name() string { return "request_validation" }

func (*RequestValidation) Definition() *pyjson.Object { return LegacyDefinition("request_validation") }

func (*RequestValidation) Execute(_ context.Context, call Call, _ Turn) (any, error) {
	opts, _ := call.Arg("options")
	if !truthy(opts) {
		opts = []any{}
	}
	q, _ := call.Arg("question")
	p, _ := call.Arg("parameter_name")
	return Obj("_validation_request", true, "question", q, "parameter_name", p, "options", opts), nil
}

// identify_speaker (core/tools/identify_speaker_tool.py): "who am I?". D2/D3: it reports only
// the speaker jarvisd identified in this conversation, never a node-claimed or earlier one.

// IdentifySpeaker is the identify_speaker tool.
type IdentifySpeaker struct{}

// NewIdentifySpeaker returns the tool.
func NewIdentifySpeaker() *IdentifySpeaker { return &IdentifySpeaker{} }

func (*IdentifySpeaker) Name() string { return "identify_speaker" }

func (*IdentifySpeaker) Definition() *pyjson.Object { return LegacyDefinition("identify_speaker") }

// unknownSpeakerNames are the placeholder names that count as unknown.
var unknownSpeakerNames = map[string]bool{"default": true, "user": true, "": true}

func (*IdentifySpeaker) Execute(_ context.Context, _ Call, turn Turn) (any, error) {
	if turn.ConversationID == "" {
		return Obj("speaker_name", nil, "error", "no_conversation",
			"message", "I don't have an active session to identify you from."), nil
	}
	name := strings.TrimSpace(turn.Speaker.Name)
	if !turn.Speaker.Known() || unknownSpeakerNames[strings.ToLower(name)] {
		msg := "I don't recognize your voice yet. Enroll a voice profile and I'll know you next time."
		if turn.Speaker.RecognitionOff {
			// M14: say why, rather than suggesting an enrollment that can't help.
			msg = "Speaker recognition is off, so I can't tell who's speaking. A household admin can turn it on in settings."
		}
		return Obj("speaker_name", nil, "message", msg), nil
	}
	return Obj("speaker_name", name), nil
}

// truthy is Python truthiness for decoded JSON values.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case *pyjson.Object:
		return x.Len() > 0
	case float64:
		return x != 0
	}
	return pyjson.Repr(v) != "0"
}
