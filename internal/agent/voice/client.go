package voice

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// Server is the voice half of command-center as a node sees it (go-pi-node.md §1.1 rows
// 7-14). HTTPServer is the real one; tests use a fake.
type Server interface {
	// StartConversation registers the node's client tools for this conversation (row 7).
	StartConversation(ctx context.Context, conversationID string, nodeContext map[string]any, tools ToolSchemas) error
	EndConversation(ctx context.Context, conversationID string) error
	Transcribe(ctx context.Context, command, speaker []byte, conversationID string) (Transcript, error)
	// VoiceStream posts the transcript. The caller must Close a returned Audio.
	VoiceStream(ctx context.Context, req StreamRequest) (Reply, error)
	// Speak renders text (the 202 "complete" path). The caller must Close the Audio.
	Speak(ctx context.Context, text string) (*Audio, error)
	// ContinueTools posts client tool results or a validation answer (rows 18-19, the
	// blocking /voice/command/continue) and returns what command-center wants next: more tool
	// calls, another question, or the final reply.
	ContinueTools(ctx context.Context, conversationID string, results []ToolResult) (*Control, error)
	// VoiceCommand is the blocking POST /voice/command: a follow-up turn (row 16).
	VoiceCommand(ctx context.Context, req StreamRequest) (*Control, error)
}

// ToolSchemas is what a node offers at /conversation/start: OpenAI-style client_tools and
// their available_commands (CommandDefinition) twins, as node.ToolDef renders them.
type ToolSchemas struct {
	ClientTools []any
	Commands    []any
}

// ToolCall is one entry of a 202's tool_calls. Arguments is the model's JSON string (or an
// object); node.DecodeArgs reads either.
type ToolCall struct {
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments any    `json:"arguments"`
	} `json:"function"`
	// FailureMessage is the command's own "what to say if this fails", when it has one.
	FailureMessage *string `json:"failure_message"`
}

// ToolResult is one {tool_call_id, output} posted back (ToolResultRequest.tool_results).
type ToolResult struct {
	ToolCallID string         `json:"tool_call_id"`
	Output     map[string]any `json:"output"`
}

// Transcript is POST /media/whisper/transcribe's answer (the fields the node uses).
type Transcript struct {
	Text   string         `json:"text"`
	Affect map[string]any `json:"affect,omitempty"`
	// Speaker is who spoke, when speaker recognition knows (tool calls get the user id).
	Speaker *struct {
		UserID *int64 `json:"user_id"`
	} `json:"speaker,omitempty"`
}

// StreamRequest is the VoiceCommandRequest body of /voice/command/stream and /voice/command.
type StreamRequest struct {
	VoiceCommand      string         `json:"voice_command"`
	ConversationID    string         `json:"conversation_id"`
	TurnSource        string         `json:"turn_source,omitempty"` // "wake" or "follow_up"
	WakeConfidence    *float64       `json:"wake_confidence,omitempty"`
	Affect            map[string]any `json:"affect,omitempty"`
	FollowUpIteration *int           `json:"follow_up_iteration,omitempty"` // 1-based
	// LevelVsWake is a follow-up's speech level against the woken command's, in dB: the
	// follow-up gate's level signal (uploads are normalised, so only the node knows).
	LevelVsWake *float64 `json:"level_vs_wake_db,omitempty"`
	// SpeechLevel is a wake turn's speech level in dBFS: with WakeConfidence, how
	// command-center picks the node that answers when several heard one wake.
	SpeechLevel *float64 `json:"speech_level_db,omitempty"`
}

// Reply is either streamed speech (200) or a control response (202).
type Reply struct {
	Audio   *Audio
	Control *Control
}

// Control is a VoiceCommandResponse (the 202 of the stream route, the 200 of the blocking
// ones), the fields the node acts on.
type Control struct {
	StopReason       string             `json:"stop_reason"`
	AssistantMessage *string            `json:"assistant_message"`
	ToolCalls        []ToolCall         `json:"tool_calls"`
	Validation       *ValidationRequest `json:"validation_request"`
	// EndOfExchange: the model marked the reply terminal (<exchange_complete/>), so no
	// follow-up window (null on the wire means false).
	EndOfExchange bool `json:"end_of_exchange"`
	// KeepListening comes with a follow-up command-center's gate rejected (not_for_me): the
	// window stays open. Absent (false) from servers without the gate: not_for_me closes it.
	KeepListening bool `json:"keep_listening"`
	// StoodDown comes with not_for_me when another node of the household answers this wake.
	StoodDown bool `json:"stood_down"`
	// Commands carries a processing error on the blocking routes (stop_reason null on
	// /continue, "complete" with no message on /voice/command).
	Commands []struct {
		Errors *struct {
			Message string `json:"message"`
		} `json:"errors"`
	} `json:"commands"`
}

// Audio is s16le PCM to play: a streamed audio/raw body, or a WAV's data chunk.
type Audio struct {
	Rate, Channels int
	// Message is X-Assistant-Message, URL-unquoted (what is being said).
	Message string
	Body    io.ReadCloser
}

func (a *Audio) Close() error { return a.Body.Close() }

// HTTPServer talks to command-center with the node's X-API-Key.
type HTTPServer struct {
	BaseURL string
	Creds   node.Creds
	// HTTP has no overall timeout (a streamed reply can be long); calls bound themselves.
	HTTP *http.Client
}

// Per-call limits. The Python node uses requests timeouts of 30 s (start, stream) and 60 s
// (STT); jarvisd's warmup can take up to 120 s, and only the first 10 s of it are waited for
// (Config.JoinWarmup), so start gets the full window in the background.
const (
	startTimeout      = 130 * time.Second
	endTimeout        = 10 * time.Second
	sttTimeout        = 60 * time.Second
	continueTimeout   = 60 * time.Second // the Python node waits 30 s; an LLM round can be slower
	speakTimeout      = 60 * time.Second
	streamHeadTimeout = 60 * time.Second
	// speakStreamOver: replies longer than this use /media/tts/speak/stream (the Python
	// node's split, N:utils/command_execution_service.py).
	speakStreamOver = 200
)

// NewHTTPServer returns a client for command-center at baseURL.
func NewHTTPServer(baseURL string, c node.Creds) *HTTPServer {
	return &HTTPServer{BaseURL: strings.TrimRight(baseURL, "/"), Creds: c,
		HTTP: &http.Client{Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: streamHeadTimeout,
			IdleConnTimeout: 90 * time.Second, MaxIdleConnsPerHost: 4,
		}}}
}

func (s *HTTPServer) request(ctx context.Context, path, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-API-Key", s.Creds.APIKey())
	return s.HTTP.Do(req)
}

func (s *HTTPServer) postJSON(ctx context.Context, path string, in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	resp, err := s.request(ctx, path, "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		return httpError(path, resp.StatusCode, raw)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// httpError keeps jarvisd's "detail" and never the request (which carries the key).
func httpError(path string, status int, raw []byte) error {
	he := &node.HTTPError{Method: http.MethodPost, Path: path, Status: status}
	var d struct {
		Detail any `json:"detail"`
	}
	if json.Unmarshal(raw, &d) == nil && d.Detail != nil {
		he.Detail = fmt.Sprint(d.Detail)
	}
	return he
}

// StartConversation is POST /conversation/start with the node's client tools (empty lists
// when it offers none).
func (s *HTTPServer) StartConversation(ctx context.Context, id string, nodeContext map[string]any, tools ToolSchemas) error {
	ctx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	var out struct {
		Status string `json:"status"`
	}
	err := s.postJSON(ctx, "/api/v0/conversation/start", map[string]any{
		"conversation_id": id, "node_context": nodeContext,
		"available_commands": orEmpty(tools.Commands), "client_tools": orEmpty(tools.ClientTools),
	}, &out)
	if err == nil && out.Status != "success" {
		err = fmt.Errorf("conversation/start: status %q", out.Status)
	}
	return err
}

func orEmpty(l []any) []any {
	if l == nil {
		return []any{}
	}
	return l
}

// ContinueTools is the blocking POST /voice/command/continue. The streaming twin
// (/continue/stream, which the Python node tries first) can't chain: it only speaks the
// post-results answer, so a model that has to list windows before minimising one could never
// make its second call. The blocking route re-enters the tool loop on native-tools models.
func (s *HTTPServer) ContinueTools(ctx context.Context, id string, results []ToolResult) (*Control, error) {
	ctx, cancel := context.WithTimeout(ctx, continueTimeout)
	defer cancel()
	if results == nil {
		results = []ToolResult{}
	}
	var c Control
	if err := s.postJSON(ctx, "/api/v0/voice/command/continue", map[string]any{
		"conversation_id": id, "tool_results": results,
	}, &c); err != nil {
		return nil, err
	}
	if c.StopReason == "" {
		// A processing error is a 200 with stop_reason null and the reason in commands.
		msg := c.commandError()
		if msg == "" {
			msg = "no stop_reason"
		}
		return nil, fmt.Errorf("voice/command/continue: %s", msg)
	}
	return &c, nil
}

// commandError is the first error a response's commands carry ("" when none).
func (c *Control) commandError() string {
	for _, cmd := range c.Commands {
		if cmd.Errors != nil && cmd.Errors.Message != "" {
			return cmd.Errors.Message
		}
	}
	return ""
}

// VoiceCommand is the blocking POST /voice/command, which the Python node uses for follow-up
// turns (continue_conversation): the full reply as JSON, spoken afterwards through TTS.
func (s *HTTPServer) VoiceCommand(ctx context.Context, req StreamRequest) (*Control, error) {
	ctx, cancel := context.WithTimeout(ctx, continueTimeout)
	defer cancel()
	var c Control
	if err := s.postJSON(ctx, "/api/v0/voice/command", req, &c); err != nil {
		return nil, err
	}
	if msg := c.commandError(); msg != "" && c.StopReason != "error" {
		// A processing error is a 200 "complete" with the reason in commands.
		return nil, fmt.Errorf("voice/command: %s", msg)
	}
	if c.StopReason == "" {
		return nil, errors.New("voice/command: no stop_reason")
	}
	return &c, nil
}

// EndConversation is POST /conversation/end.
func (s *HTTPServer) EndConversation(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, endTimeout)
	defer cancel()
	return s.postJSON(ctx, "/api/v0/conversation/end", map[string]any{"conversation_id": id}, nil)
}

// Transcribe is POST /media/whisper/transcribe: multipart file (the command), speaker_audio
// (wake clip + command; must start with the wake audio) and conversation_id.
func (s *HTTPServer) Transcribe(ctx context.Context, command, speaker []byte, id string) (Transcript, error) {
	ctx, cancel := context.WithTimeout(ctx, sttTimeout)
	defer cancel()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part := func(field, filename string, data []byte) error {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, field, filename))
		h.Set("Content-Type", "audio/wav")
		w, err := mw.CreatePart(h)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	}
	if err := part("file", "command.wav", command); err != nil {
		return Transcript{}, err
	}
	if id != "" {
		if err := mw.WriteField("conversation_id", id); err != nil {
			return Transcript{}, err
		}
	}
	if len(speaker) > 0 {
		if err := part("speaker_audio", "speaker.wav", speaker); err != nil {
			return Transcript{}, err
		}
	}
	if err := mw.Close(); err != nil {
		return Transcript{}, err
	}
	const path = "/api/v0/media/whisper/transcribe"
	resp, err := s.request(ctx, path, mw.FormDataContentType(), &body)
	if err != nil {
		return Transcript{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		return Transcript{}, httpError(path, resp.StatusCode, raw)
	}
	var t Transcript
	return t, json.Unmarshal(raw, &t)
}

// EnrollVoice is M4: POST /media/whisper/voice-profiles/enroll?user_id=…, the take as
// multipart file. A take jarvisd finds too poor is a 422 *node.HTTPError.
func (s *HTTPServer) EnrollVoice(ctx context.Context, userID int64, wav []byte) (map[string]any, error) {
	var out map[string]any
	q := url.Values{"user_id": {strconv.FormatInt(userID, 10)}}
	return out, s.uploadWAV(ctx, "/api/v0/media/whisper/voice-profiles/enroll", q, wav, &out)
}

// VerifyVoice is M5: POST /media/whisper/voice-profiles/verify?user_id=…&household_id=….
func (s *HTTPServer) VerifyVoice(ctx context.Context, userID int64, householdID string, wav []byte) (VerifyResult, error) {
	var out VerifyResult
	q := url.Values{"user_id": {strconv.FormatInt(userID, 10)}, "household_id": {householdID}}
	return out, s.uploadWAV(ctx, "/api/v0/media/whisper/voice-profiles/verify", q, wav, &out)
}

// PostVoiceProfileResult is V9: a take's outcome, for the app polling V10.
func (s *HTTPServer) PostVoiceProfileResult(ctx context.Context, rid string, result map[string]any) error {
	ctx, cancel := context.WithTimeout(ctx, endTimeout)
	defer cancel()
	return s.postJSON(ctx, "/api/v0/mobile/voice-profile-results/"+url.PathEscape(rid), result, nil)
}

// uploadWAV posts wav as the multipart field "file" and decodes the JSON answer into out.
func (s *HTTPServer) uploadWAV(ctx context.Context, path string, q url.Values, wav []byte, out any) error {
	ctx, cancel := context.WithTimeout(ctx, sttTimeout)
	defer cancel()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="voice.wav"`)
	h.Set("Content-Type", "audio/wav")
	w, err := mw.CreatePart(h)
	if err != nil {
		return err
	}
	if _, err := w.Write(wav); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}
	resp, err := s.request(ctx, path+"?"+q.Encode(), mw.FormDataContentType(), &body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		return httpError(path, resp.StatusCode, raw)
	}
	return json.Unmarshal(raw, out)
}

// VoiceStream is POST /voice/command/stream: 200 audio/raw with X-Audio-* headers, or 202
// JSON. ctx governs the whole body, so cancelling it stops a stream mid-reply.
func (s *HTTPServer) VoiceStream(ctx context.Context, req StreamRequest) (Reply, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return Reply{}, err
	}
	const path = "/api/v0/voice/command/stream"
	resp, err := s.request(ctx, path, "application/json", bytes.NewReader(b))
	if err != nil {
		return Reply{}, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		a, err := rawAudio(resp)
		if err != nil {
			resp.Body.Close()
			return Reply{}, err
		}
		return Reply{Audio: a}, nil
	case http.StatusAccepted:
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		var c Control
		if err := json.Unmarshal(raw, &c); err != nil {
			return Reply{}, fmt.Errorf("voice/command/stream: bad 202 body: %w", err)
		}
		return Reply{Control: &c}, nil
	default:
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return Reply{}, httpError(path, resp.StatusCode, raw)
	}
}

// rawAudio reads the X-Audio-* headers. jarvisd always sends them; the defaults are the
// Python node's (22050 Hz mono 16-bit).
func rawAudio(resp *http.Response) (*Audio, error) {
	hdr := func(k string, def int) (int, error) {
		v := resp.Header.Get(k)
		if v == "" {
			return def, nil
		}
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("bad %s %q", k, v)
		}
		return n, nil
	}
	rate, err := hdr("X-Audio-Sample-Rate", 22050)
	if err != nil {
		return nil, err
	}
	ch, err := hdr("X-Audio-Channels", 1)
	if err != nil {
		return nil, err
	}
	width, err := hdr("X-Audio-Sample-Width", 2)
	if err != nil {
		return nil, err
	}
	if width != 2 {
		return nil, fmt.Errorf("unsupported %d-byte samples (want 16-bit)", width)
	}
	msg, _ := url.PathUnescape(resp.Header.Get("X-Assistant-Message"))
	return &Audio{Rate: rate, Channels: ch, Message: msg, Body: resp.Body}, nil
}

// Speak renders text: /media/tts/speak/stream (audio/raw) over 200 characters, else
// /media/tts/speak (one WAV), as the Python node does.
func (s *HTTPServer) Speak(ctx context.Context, text string) (*Audio, error) {
	path := "/api/v0/media/tts/speak"
	streamed := utf8.RuneCountInString(text) > speakStreamOver
	if streamed {
		path += "/stream"
	}
	b, _ := json.Marshal(map[string]string{"text": text})
	resp, err := s.request(ctx, path, "application/json", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, httpError(path, resp.StatusCode, raw)
	}
	var a *Audio
	if streamed {
		a, err = rawAudio(resp)
	} else {
		a, err = wavAudio(resp.Body)
	}
	if err != nil {
		resp.Body.Close()
		return nil, err
	}
	a.Message = text
	return a, nil
}

type readCloser struct {
	io.Reader
	io.Closer
}

// wavAudio reads a RIFF header up to the data chunk and returns the rest as the PCM body.
func wavAudio(body io.ReadCloser) (*Audio, error) {
	r := bufio.NewReader(body)
	var riff [12]byte
	if _, err := io.ReadFull(r, riff[:]); err != nil {
		return nil, fmt.Errorf("tts: wav header: %w", err)
	}
	if string(riff[0:4]) != "RIFF" || string(riff[8:12]) != "WAVE" {
		return nil, errors.New("tts: reply is not a WAV")
	}
	var rate, ch int
	for {
		var h [8]byte
		if _, err := io.ReadFull(r, h[:]); err != nil {
			return nil, fmt.Errorf("tts: wav has no data chunk: %w", err)
		}
		id, size := string(h[0:4]), int64(binary.LittleEndian.Uint32(h[4:8]))
		switch id {
		case "fmt ":
			f := make([]byte, size)
			if _, err := io.ReadFull(r, f); err != nil || size < 16 {
				return nil, errors.New("tts: bad wav fmt chunk")
			}
			ch = int(binary.LittleEndian.Uint16(f[2:4]))
			rate = int(binary.LittleEndian.Uint32(f[4:8]))
			if bits := binary.LittleEndian.Uint16(f[14:16]); bits != 16 || ch < 1 || rate < 1 {
				return nil, fmt.Errorf("tts: unsupported wav (%d-bit, %d ch, %d Hz)", bits, ch, rate)
			}
		case "data":
			if rate == 0 {
				return nil, errors.New("tts: wav data before fmt")
			}
			return &Audio{Rate: rate, Channels: ch, Body: readCloser{io.LimitReader(r, size), body}}, nil
		default:
			if _, err := io.CopyN(io.Discard, r, size+size%2); err != nil {
				return nil, err
			}
		}
	}
}
