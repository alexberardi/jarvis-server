// device-node is a disposable protocol stand-in for the browser suite. It exercises the
// real HTTP gateway, MQTT delivery and node-authenticated results; it never scans a LAN.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	var cfg struct{ URL, Broker, ID, Key, Entity string }
	if err := json.Unmarshal([]byte(os.Getenv("DEVICE_NODE_CONFIG")), &cfg); err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	request := func(method, path string, body any, out any) error {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r, err := http.NewRequest(method, cfg.URL+path, bytes.NewReader(b))
		if err != nil {
			return err
		}
		r.Header.Set("X-API-Key", cfg.ID+":"+cfg.Key)
		r.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(r)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("fixture %s %s: HTTP %d", method, path, resp.StatusCode)
		}
		if out != nil {
			return json.NewDecoder(resp.Body).Decode(out)
		}
		return nil
	}
	var creds struct{ Username, Password string }
	if err := request("GET", "/api/v0/node/mqtt-credentials", nil, &creds); err != nil {
		return err
	}
	c := mqtt.NewClient(mqtt.NewClientOptions().AddBroker("tcp://" + cfg.Broker).SetClientID("jarvis-node-" + cfg.ID).
		SetUsername(creds.Username).SetPassword(creds.Password).SetCleanSession(true).SetConnectTimeout(10 * time.Second))
	if t := c.Connect(); !t.WaitTimeout(10*time.Second) || t.Error() != nil {
		return fmt.Errorf("fixture MQTT connect: %v", t.Error())
	}
	defer c.Disconnect(100)
	var paired atomic.Bool
	prefix := "jarvis/nodes/" + cfg.ID + "/"
	errors := make(chan error, 1)
	t := c.Subscribe(prefix+"#", 1, func(_ mqtt.Client, msg mqtt.Message) {
		var p struct {
			RequestID string         `json:"request_id"`
			Action    string         `json:"action_name"`
			Command   string         `json:"command_name"`
			Context   map[string]any `json:"context"`
		}
		payload := msg.Payload()
		if strings.HasSuffix(msg.Topic(), "/commands") {
			var commands []struct {
				Details json.RawMessage `json:"details"`
			}
			if json.Unmarshal(payload, &commands) != nil || len(commands) != 1 {
				return
			}
			payload = commands[0].Details
		}
		if json.Unmarshal(payload, &p) != nil {
			return
		}
		var path string
		var result any
		switch strings.TrimPrefix(msg.Topic(), prefix) {
		case "device-scan":
			path = "/api/v0/nodes/" + cfg.ID + "/device-scan/" + p.RequestID + "/results"
			result = map[string]any{"devices": []any{map[string]any{"entity_id": cfg.Entity, "name": "Test thermostat", "domain": "climate", "protocol": "browser_fixture", "manufacturer": "Test integration"}}}
		case "device-state":
			path = "/api/v0/device-state-results/" + p.RequestID
			result = map[string]any{"state": map[string]any{"needs_pairing": !paired.Load()}, "ui_hints": map[string]any{}}
		case "commands":
			if p.Command != "control_device" {
				return
			}
			path = "/api/v0/device-control-results/" + p.RequestID
			if p.Action == "pair_start" {
				result = map[string]any{"success": false, "input_required": map[string]any{"type": "pin", "prompt": "Enter the fixture code", "session_id": "browser-session", "follow_up_action": "pair_finish"}}
			} else {
				ok := p.Action == "pair_finish" && p.Context["session_id"] == "browser-session" && p.Context["pin"] == "012-345" && p.Context["entity_id"] == cfg.Entity
				paired.Store(ok)
				result = map[string]any{"success": ok, "error": "Incorrect fixture answer"}
			}
		default:
			return
		}
		if err := request("POST", path, result, nil); err != nil {
			select {
			case errors <- err:
			default:
			}
		}
	})
	if !t.WaitTimeout(10*time.Second) || t.Error() != nil {
		return fmt.Errorf("fixture MQTT subscribe: %v", t.Error())
	}
	beat := func() error {
		return request("POST", "/api/v0/admin/nodes/heartbeat", map[string]any{"node_kind": "desktop", "protocols": []string{"browser_fixture"}}, nil)
	}
	if err := beat(); err != nil {
		return err
	}
	fmt.Println("ready")
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-signals:
			return nil
		case err := <-errors:
			return err
		case <-ticker.C:
			if err := beat(); err != nil {
				return err
			}
		}
	}
}
