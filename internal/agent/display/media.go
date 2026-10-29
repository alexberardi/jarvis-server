package display

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/host"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	shared "github.com/alexberardi/jarvis-server/internal/display"
)

type MediaRequest struct {
	Action      string  `json:"action"`
	URL         string  `json:"url"`
	Query       string  `json:"query"`
	Destination string  `json:"destination"`
	Seconds     float64 `json:"seconds"`
}

func SendIPC(socket string, command []any) (any, error) {
	conn, err := net.DialTimeout("unix", socket, 300*time.Millisecond)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(500 * time.Millisecond))
	if err = json.NewEncoder(conn).Encode(map[string]any{"command": command, "request_id": 1}); err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		var out map[string]any
		if json.Unmarshal(scanner.Bytes(), &out) == nil && out["request_id"] == float64(1) {
			if out["error"] != "success" {
				return nil, errors.New("media command failed")
			}
			return out["data"], nil
		}
	}
	return nil, errors.New("media player did not answer")
}
func (e *Engine) MediaSockets() []string {
	sockets := []string{filepath.Join(e.root, "mpv.sock")}
	markers, _ := filepath.Glob(filepath.Join(e.root, "receiver-*.json"))
	for _, p := range markers {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var v struct {
			Socket string `json:"socket"`
		}
		if json.Unmarshal(b, &v) == nil && v.Socket != "" {
			sockets = append(sockets, v.Socket)
		}
	}
	return sockets
}
func (e *Engine) DuckMedia() {
	e.mediaMu.Lock()
	defer e.mediaMu.Unlock()
	e.mu.Lock()
	was := e.autoPaused
	active := e.state == "listening" || e.state == "thinking" || e.state == "speaking"
	e.mu.Unlock()
	if active {
		paused := []string{}
		for _, socket := range e.MediaSockets() {
			v, err := SendIPC(socket, []any{"get_property", "pause"})
			if err == nil && v == false {
				if _, err = SendIPC(socket, []any{"set_property", "pause", true}); err == nil {
					paused = append(paused, socket)
				}
			}
		}
		e.mu.Lock()
		e.autoPaused = true
		e.ducked = append(e.ducked, paused...)
		e.mu.Unlock()
		if !was && len(paused) > 0 && e.Host != nil {
			windows, err := e.Host.ListWindows(e.ctx)
			if err == nil {
				displayWindow, mediaWindow := "", ""
				for _, w := range windows {
					if strings.Contains(w.App, "JarvisDisplay") {
						displayWindow = w.ID
					}
					if strings.Contains(w.App, "mpv") {
						mediaWindow = w.ID
					}
				}
				if displayWindow != "" {
					e.mu.Lock()
					e.returnWindow = mediaWindow
					e.mu.Unlock()
					_ = e.Host.WindowDo(e.ctx, host.ActionFocus, displayWindow)
				}
			}
		}
	} else if !active && was {
		e.mu.Lock()
		sockets := e.ducked
		e.autoPaused = false
		e.ducked = nil
		e.mu.Unlock()
		for _, socket := range sockets {
			_, _ = SendIPC(socket, []any{"set_property", "pause", false})
		}
		e.mu.Lock()
		window := e.returnWindow
		e.returnWindow = ""
		e.mu.Unlock()
		if window != "" && e.Host != nil && e.GetPlaybackStatus()["active"] == true {
			_ = e.Host.WindowDo(e.ctx, host.ActionFocus, window)
		}
	}
}
func (e *Engine) GetPlaybackStatus() map[string]any {
	e.mu.Lock()
	kind := e.mediaKind
	running := e.media != nil
	e.mu.Unlock()
	out := map[string]any{"active": running, "kind": kind, "controllable": kind == "mpv"}
	for _, socket := range e.MediaSockets() {
		idle, err := SendIPC(socket, []any{"get_property", "idle-active"})
		if err == nil && idle == false {
			out["active"] = true
			out["controllable"] = true
			v, _ := SendIPC(socket, []any{"get_property", "pause"})
			out["paused"] = v
			v, _ = SendIPC(socket, []any{"get_property", "time-pos"})
			out["position"] = v
			v, _ = SendIPC(socket, []any{"get_property", "duration"})
			out["duration"] = v
			out["socket"] = socket
			break
		}
	}
	return out
}
func (e *Engine) ControlMedia(ctx context.Context, r MediaRequest) (map[string]any, error) {
	e.mediaMu.Lock()
	defer e.mediaMu.Unlock()
	e.mu.Lock()
	cfg := e.delivery.Configuration
	cc := e.cc
	e.mu.Unlock()
	if !cfg.Enabled {
		return nil, errors.New("display mode is disabled")
	}
	if r.Action == "status" {
		status := e.GetPlaybackStatus()
		delete(status, "socket")
		return status, nil
	}
	if r.Destination == "" {
		r.Destination = cfg.Destination
	}
	if r.Destination != "local" {
		if cc == nil {
			return nil, errors.New("destination unavailable; choose local playback")
		}
		var out map[string]any
		err := cc.Do(ctx, "POST", "/api/v0/node/display/media", r, &out)
		if err != nil {
			return nil, fmt.Errorf("destination unavailable; choose local playback: %w", err)
		}
		if out["success"] == false {
			return nil, errors.New("destination rejected playback; choose local playback")
		}
		return out, nil
	}
	if r.Action != "play" {
		status := e.GetPlaybackStatus()
		socket, _ := status["socket"].(string)
		if r.Action == "stop" {
			e.mu.Lock()
			e.StopMedia()
			e.mu.Unlock()
			if socket != "" {
				_, _ = SendIPC(socket, []any{"stop"})
			}
			return map[string]any{"success": true}, nil
		}
		var command []any
		switch r.Action {
		case "pause":
			command = []any{"set_property", "pause", true}
		case "resume":
			command = []any{"set_property", "pause", false}
		case "seek":
			if r.Seconds < 0 || r.Seconds > 864000 {
				return nil, errors.New("invalid seek position")
			}
			command = []any{"seek", r.Seconds, "absolute+exact"}
		default:
			return nil, errors.New("unsupported media action")
		}
		if socket == "" {
			return nil, errors.New("this streaming website is controlled by touch")
		}
		if _, err := SendIPC(socket, command); err != nil {
			return nil, err
		}
		e.mu.Lock()
		e.mediaPaused = r.Action == "pause"
		if r.Action == "pause" {
			e.ducked = nil
		}
		e.mu.Unlock()
		return map[string]any{"success": true}, nil
	}
	target := r.URL
	website := false
	if r.Query != "" {
		target = "https://www.youtube.com/results?search_query=" + url.QueryEscape(r.Query)
		website = true
	}
	for _, s := range cfg.Shortcuts {
		if strings.EqualFold(target, s.Name) {
			target = s.URL
			website = true
		}
	}
	if shared.WebURL(target) {
		u, _ := url.Parse(target)
		host := strings.ToLower(u.Hostname())
		if host == "youtube.com" || strings.HasSuffix(host, ".youtube.com") || host == "youtu.be" {
			website = true
		}
	} else {
		home, _ := os.UserHomeDir()
		allowed := filepath.Join(home, "Videos")
		real, err := filepath.EvalSymlinks(target)
		if err != nil {
			return nil, errors.New("use an HTTP(S) link, a streaming shortcut or a file in Videos")
		}
		rel, err := filepath.Rel(allowed, real)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, errors.New("local video must be inside Videos")
		}
		st, err := os.Stat(real)
		if err != nil || !st.Mode().IsRegular() {
			return nil, errors.New("local video is not a regular file")
		}
		target = real
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.StopMedia()
	var cmd *exec.Cmd
	if website {
		cmd = exec.CommandContext(e.ctx, "chromium", "--no-first-run", "--user-data-dir="+filepath.Join(e.root, "streaming-browser"), "--start-fullscreen", "--app="+target)
		e.mediaKind = "website"
	} else {
		_ = os.Remove(filepath.Join(e.root, "mpv.sock"))
		cmd = exec.CommandContext(e.ctx, "mpv", "--no-config", "--load-scripts=no", "--ytdl=no", "--fullscreen", "--input-ipc-server="+filepath.Join(e.root, "mpv.sock"), "--", target)
		e.mediaKind = "mpv"
	}
	SetProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		e.mediaKind = ""
		return nil, errors.New("media player could not start")
	}
	e.media = cmd
	time.AfterFunc(200*time.Millisecond, func() {
		if e.ctx.Err() == nil {
			e.DuckMedia()
		}
	})
	e.lastInteraction = time.Now()
	go func() {
		_ = cmd.Wait()
		e.mu.Lock()
		if e.media == cmd {
			e.media = nil
			e.mediaKind = ""
			e.lastInteraction = time.Now()
		}
		e.mu.Unlock()
	}()
	return map[string]any{"success": true, "kind": e.mediaKind}, nil
}
func (e *Engine) Available(context.Context) []node.ToolDef {
	e.mu.Lock()
	enabled := e.delivery.Configuration.Enabled
	e.mu.Unlock()
	if !enabled {
		return nil
	}
	return []node.ToolDef{(node.Spec{Name: "display_media", Description: "Play a video on this Jarvis display by default, or a named household media destination. Use query for YouTube search results for touch selection. Subscription shortcut names open the website for touch navigation.", Params: []node.Param{{Name: "action", Type: "string", Required: true, Enum: []string{"play", "pause", "resume", "seek", "stop", "status"}}, {Name: "url", Type: "string", Description: "Direct video URL, YouTube URL, streaming shortcut name or file in Videos"}, {Name: "query", Type: "string", Description: "YouTube search words"}, {Name: "destination", Type: "string", Description: "local or an advertised household media destination name"}, {Name: "seconds", Type: "float", Description: "Absolute seek position in seconds"}}}).Render()}
}
func (e *Engine) Call(ctx context.Context, name string, args map[string]any, _ node.Request) (map[string]any, bool) {
	if name != "display_media" {
		return nil, false
	}
	b, _ := json.Marshal(args)
	var r MediaRequest
	if err := json.Unmarshal(b, &r); err != nil {
		return map[string]any{"success": false, "error": "Invalid media arguments"}, true
	}
	out, err := e.ControlMedia(ctx, r)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}, true
	}
	return out, true
}

// RunReceiverPlayer is the mpv wrapper used by both receiver processes. Its private marker
// lets the shell and voice indicator share the receiver's existing IPC without replacing it.
func RunReceiverPlayer(ctx context.Context, args []string) error {
	root := os.Getenv("JARVIS_DISPLAY_ROOT")
	if root == "" {
		return errors.New("display receiver context missing")
	}
	socket := ""
	for i, a := range args {
		if strings.HasPrefix(a, "--input-ipc-server=") {
			socket = strings.TrimPrefix(a, "--input-ipc-server=")
		}
		if a == "--input-ipc-server" && i+1 < len(args) {
			socket = args[i+1]
		}
	}
	marker := filepath.Join(root, "receiver-"+strconv.Itoa(os.Getpid())+".json")
	if socket == "" {
		socket = strings.TrimSuffix(marker, ".json") + ".sock"
		args = append([]string{"--input-ipc-server=" + socket}, args...)
	}
	args = append([]string{"--no-config", "--load-scripts=no", "--fullscreen"}, args...)
	raw, _ := json.Marshal(map[string]any{"socket": socket})
	if err := shared.WriteCache(marker, raw); err != nil {
		return err
	}
	defer os.Remove(marker)
	cmd := exec.CommandContext(ctx, "/usr/bin/mpv", args...)
	return cmd.Run()
}
