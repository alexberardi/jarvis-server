package models

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/engine"
)

// The admin's labels and hardware views carry gpu_fault: the driver mismatch detection found,
// with the labels it stops, or nothing when the GPU is fine.
func TestAPIReportsGPUFault(t *testing.T) {
	e := newEnv(t)
	fault := engine.DriverMismatchFault("580.173.04", "580.178.04")
	e.mgr.Detector.Driver = func() *engine.GPUFault { return fault }
	e.mgr.Detector.Platform = engine.Platform{OS: "linux", Arch: "amd64"}
	e.labels.status = []engine.LabelStatus{
		{Label: "live", State: engine.StateGPUUnavailable, Reason: engine.UserMsgDriverUpdated},
		{Label: "embeddings", State: "ready"},
	}
	mux := http.NewServeMux()
	(&API{Manager: e.mgr, Resolver: e.labels}).Mount(mux, func(http.ResponseWriter, *http.Request) bool { return true })
	get := func(path string) map[string]any {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil).WithContext(context.Background()))
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	for _, path := range []string{"/v1/models/labels", "/v1/hardware"} {
		f, _ := get(path)["gpu_fault"].(map[string]any)
		if f == nil || f["kind"] != engine.FaultDriverMismatch || f["kernel_version"] != "580.173.04" ||
			f["library_version"] != "580.178.04" || f["user_message"] != engine.UserMsgDriverUpdated ||
			f["message"] != "NVIDIA driver updated (kernel 580.173.04, libraries 580.178.04): reboot the server." {
			t.Fatalf("%s: %v", path, f)
		}
		if ls, _ := f["labels"].([]any); len(ls) != 1 || ls[0] != "live" {
			t.Fatalf("%s labels: %v", path, f["labels"])
		}
	}

	fault = nil
	e.mgr.Detector.Hardware(context.Background(), true)
	e.labels.status = []engine.LabelStatus{{Label: "live", State: "ready"}}
	if f := get("/v1/models/labels")["gpu_fault"]; f != nil {
		t.Fatalf("healthy: %v", f)
	}
}
