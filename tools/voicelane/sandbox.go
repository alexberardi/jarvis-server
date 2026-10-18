package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/platform/config"
)

// sandboxEnv reads .sandbox/<name>/sandbox.env under root.
func sandboxEnv(root, name string) (map[string]string, error) {
	f, err := os.Open(filepath.Join(root, ".sandbox", name, "sandbox.env"))
	if err != nil {
		return nil, fmt.Errorf("sandbox %s: %w (scripts/sandbox up --name %s --voice)", name, err, name)
	}
	defer f.Close()
	kvs, err := config.ParseEnv(f)
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	for _, kv := range kvs {
		env[kv[0]] = kv[1]
	}
	for _, k := range []string{"SBX_CC", "SBX_CONFIG", "SBX_NODE_ID", "SBX_NODE_KEY"} {
		if env[k] == "" {
			return nil, fmt.Errorf("sandbox %s: sandbox.env has no %s", name, k)
		}
	}
	if env["SBX_VOICE"] == "" {
		return nil, fmt.Errorf("sandbox %s has no voice (scripts/sandbox up --voice)", name)
	}
	return env, nil
}

// node is the sandbox's seeded node, for its TTS and STT (node auth, as a Pi calls them).
type node struct {
	cc, id, key string
	http        *http.Client
}

// Speak is the house voice saying text: a WAV.
func (n *node) Speak(ctx context.Context, text string) ([]byte, error) {
	b, _ := json.Marshal(map[string]string{"text": text})
	return n.post(ctx, "/api/v0/media/tts/speak", "application/json", b)
}

// Transcribe is what the sandbox's whisper hears in a WAV.
func (n *node) Transcribe(ctx context.Context, wav []byte) (string, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "reply.wav")
	if err != nil {
		return "", err
	}
	fw.Write(wav)
	mw.Close()
	rb, err := n.post(ctx, "/api/v0/media/whisper/transcribe", mw.FormDataContentType(), body.Bytes())
	if err != nil {
		return "", err
	}
	var t struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(rb, &t); err != nil {
		return "", fmt.Errorf("transcribe: %w", err)
	}
	return strings.TrimSpace(t.Text), nil
}

func (n *node) post(ctx context.Context, path, contentType string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.cc+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-API-Key", n.id+":"+n.key)
	hc := n.http
	if hc == nil {
		hc = &http.Client{Timeout: 2 * time.Minute}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	rb, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		s := strings.TrimSpace(string(rb))
		if len(s) > 300 {
			s = s[:300]
		}
		return nil, fmt.Errorf("POST %s: %d %s", path, resp.StatusCode, s)
	}
	return rb, nil
}

// writeCreds gives the node the sandbox's seeded node identity (node.Creds) in dir.
func writeCreds(dir string, env map[string]string) (string, error) {
	b, _ := json.Marshal(map[string]any{"config_url": env["SBX_CONFIG"], "node_id": env["SBX_NODE_ID"],
		"node_key": env["SBX_NODE_KEY"], "name": "e2e voice lane", "enrolled_at": time.Now().UTC()})
	p := filepath.Join(dir, "credentials.json")
	return p, os.WriteFile(p, b, 0o600)
}

// The model's calls, from the sandbox's llm.jsonl (the tap, tools/pipeline/tap.go): only the
// reply's tool calls are read.

type tapLine struct {
	Reply struct {
		Content   string `json:"content"`
		ToolCalls []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tool_calls"`
	} `json:"reply"`
}

func fileSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}

// modelCalls reads the tap log's lines after offset: how many model calls there were and the
// tools they called (structured calls, or a text-path model's <tool_call> blocks parsed the
// way command-center parses them).
func modelCalls(path string, offset int64) (n int, tools []string) {
	f, err := os.Open(path)
	if err != nil {
		return 0, nil
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return 0, nil
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for sc.Scan() {
		var l tapLine
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}
		n++
		for _, c := range l.Reply.ToolCalls {
			tools = append(tools, c.Function.Name)
		}
		if len(l.Reply.ToolCalls) == 0 && strings.Contains(l.Reply.Content, "<tool_call>") {
			out, ok := parse.ParseQwen3(l.Reply.Content, true)
			if !ok {
				out = l.Reply.Content
			}
			for _, c := range parse.ParseToolCalls(out).ToolCalls {
				tools = append(tools, c.Function.Name)
			}
		}
	}
	return n, tools
}
