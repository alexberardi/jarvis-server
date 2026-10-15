package node

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The heartbeat answer carries jarvisd's update dispatch and, for a desktop agent, the server's
// version (01-no-cli-plan 1.7); a failed update is reported on the task.
func TestBeatReplyAndUpdateFailure(t *testing.T) {
	answer := `{"status":"ok"}`
	var sent map[string]any
	var status map[string]any
	var statusPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v0/admin/nodes/heartbeat":
			_ = json.NewDecoder(r.Body).Decode(&sent)
			w.Write([]byte(answer))
		default:
			statusPath = r.URL.Path
			_ = json.NewDecoder(r.Body).Decode(&status)
			w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	c := NewCC(srv.URL, Creds{NodeID: "n1", NodeKey: "k1", ConfigURL: srv.URL})
	ctx := context.Background()

	r, err := c.Beat(ctx, Beat{Version: "v0.1.0"})
	if err != nil || r.PendingUpdate != nil || r.ServerVersion != "" {
		t.Fatalf("plain answer: %+v %v", r, err)
	}
	if sent["is_busy"] != false {
		t.Fatalf("is_busy = %v", sent["is_busy"])
	}
	answer = `{"status":"ok","server_version":"v0.2.0","pending_update":{"task_id":"t1","target_version":"v0.2.0"}}`
	r, err = c.Beat(ctx, Beat{Version: "v0.1.0"})
	if err != nil || r.ServerVersion != "v0.2.0" || r.PendingUpdate == nil || *r.PendingUpdate != (PendingUpdate{"t1", "v0.2.0"}) {
		t.Fatalf("update answer: %+v %v", r, err)
	}
	answer = `{"status":"ok","pending_update":{"task_id":"t2","target_version":null}}`
	if r, _ = c.Beat(ctx, Beat{}); r.PendingUpdate == nil || r.PendingUpdate.TargetVersion != "" {
		t.Fatalf("null target: %+v", r.PendingUpdate)
	}
	// A status probe asks for no work.
	if _, err := c.Beat(ctx, Beat{Busy: true}); err != nil || sent["is_busy"] != true {
		t.Fatalf("busy beat: %v %v", sent["is_busy"], err)
	}
	if err := c.ReportUpdateFailed(ctx, "t/1", "not signed"); err != nil {
		t.Fatal(err)
	}
	if statusPath != "/api/v0/nodes/tasks/t/1/status" || status["state"] != "failed" || status["error_message"] != "not signed" {
		t.Fatalf("report: %s %v", statusPath, status)
	}
}

// device_id goes out only when known: a Pi (or an OS that doesn't say) sends the old body.
func TestBeatDeviceID(t *testing.T) {
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent = nil
		_ = json.NewDecoder(r.Body).Decode(&sent)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()
	c := NewCC(srv.URL, Creds{NodeID: "n1", NodeKey: "k1", ConfigURL: srv.URL})
	if _, err := c.Beat(context.Background(), Beat{NodeKind: "desktop", DeviceID: "abc"}); err != nil || sent["device_id"] != "abc" {
		t.Fatalf("device_id = %v, %v", sent["device_id"], err)
	}
	if _, err := c.Beat(context.Background(), Beat{}); err != nil {
		t.Fatal(err)
	}
	if _, has := sent["device_id"]; has {
		t.Fatalf("an empty device id was sent: %v", sent)
	}
}
