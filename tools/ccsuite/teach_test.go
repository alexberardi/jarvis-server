package main

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/control"
)

// fakeBank is the suite browser, the agent's recorder, the sites and cc's API as a
// demonstration sees them: the bank's statements list, a month's page and its download.
type fakeBank struct {
	t        *testing.T
	log      []string // what happened, in order
	url      string
	aiming   string // what the last click expression aimed at
	missing  string // a link text the page doesn't have
	download bool
	lists    int // GET /workflows calls; the workflow is learnt by the second
	put      map[string]any
}

const fakeSites = "http://127.0.0.1:9"

var learnt = map[string]any{"id": "tw_1", "recording_id": "rec_1", "name": "Download bank statement", "slug": "download_bank_statement",
	"triggers": []any{"download bank statement"}, "params": []any{map[string]any{"name": "month", "kind": "month", "example": "2026-09"}},
	"steps": []any{map[string]any{"intent": "open", "target": "browser"}}, "success": map[string]any{"download": map[string]any{"name_like": "*.csv"}},
	"shared": false, "version": 1.0, "editable": true}

func (f *fakeBank) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return f.CallSession(ctx, "", method, params)
}

func (f *fakeBank) CallSession(_ context.Context, session, method string, params any) (json.RawMessage, error) {
	p, _ := params.(map[string]any)
	switch method {
	case "Target.createTarget":
		f.log = append(f.log, "new tab")
		return json.RawMessage(`{"targetId":"T1"}`), nil
	case "Target.attachToTarget":
		if p["flatten"] != true {
			f.t.Error("attached without a flat session")
		}
		return json.RawMessage(`{"sessionId":"S1"}`), nil
	case "Target.closeTarget":
		f.log = append(f.log, "close tab")
		return json.RawMessage(`{}`), nil
	}
	if session != "S1" {
		f.t.Errorf("%s outside the tab's session (%q)", method, session)
	}
	switch method {
	case "Page.bringToFront":
		return json.RawMessage(`{}`), nil
	case "Page.navigate":
		f.url = strings.Replace(p["url"].(string), "/bank/", "/bank/statements", 1)
		f.log = append(f.log, "open "+p["url"].(string))
		return json.RawMessage(`{}`), nil
	case "Runtime.evaluate":
		expr := p["expression"].(string)
		if strings.Contains(expr, ".click(") {
			f.t.Errorf("an untrusted click: %s", expr)
		}
		var v any
		switch {
		case strings.Contains(expr, "readyState"):
			v = map[string]string{"URL": f.url, "State": "complete"}
		case strings.Contains(expr, "querySelectorAll"):
			f.aiming = expr
			v = map[string]float64{"X": 40, "Y": 120}
			if f.missing != "" && strings.Contains(expr, f.missing) {
				v = nil
			}
		default:
			v = f.url
		}
		b, _ := json.Marshal(map[string]any{"result": map[string]any{"value": v}})
		return b, nil
	case "Input.dispatchMouseEvent":
		if p["type"] == "mouseReleased" {
			switch {
			case strings.Contains(f.aiming, `"Statement 2026-09"`):
				f.log = append(f.log, "click Statement 2026-09")
				f.url = fakeSites + "/bank/statements/2026-09"
			case strings.Contains(f.aiming, `"Download CSV"`):
				f.log = append(f.log, "click Download CSV")
				f.download = true
			}
		}
		return json.RawMessage(`{}`), nil
	}
	return nil, fmt.Errorf("%s: unexpected", method)
}

func (f *fakeBank) teacher(clk *fakeClock) *teacher {
	return &teacher{cdp: f, sites: fakeSites, household: "hh1", clk: clk.clock(), settle: time.Second,
		record: func(cmd string) (string, error) {
			f.log = append(f.log, cmd)
			if cmd == control.ComputerRecordStart {
				return "rec_1", nil
			}
			return "rec_1 4", nil
		},
		sitesRaw: func(context.Context) ([]byte, error) {
			if f.download {
				return []byte(`{"downloads": [{"site": "bank", "month": "2026-09", "filename": "statement-2026-09.csv"}]}`), nil
			}
			return []byte(`{"downloads": []}`), nil
		},
		api: func(_ context.Context, method, path string, body, out any) error {
			f.log = append(f.log, method+" "+path)
			switch {
			case method == "GET" && path == "/workflows?household_id=hh1":
				f.lists++
				list := []any{}
				if f.lists >= 2 {
					list = append(list, learnt)
				}
				b, _ := json.Marshal(map[string]any{"workflows": list})
				return json.Unmarshal(b, out)
			case method == "PUT":
				f.put = body.(map[string]any)
			}
			return nil
		}}
}

// TestDemonstrate: the demonstration is recorded from before the first page to after the
// download, with real input events in a tab of its own; the workflow learnt from it is saved
// with its editable fields as they are; forget deletes the workflow and the recording.
func TestDemonstrate(t *testing.T) {
	f := &fakeBank{t: t}
	tc := f.teacher(&fakeClock{t: time.Unix(0, 0)})
	var got taught
	if err := tc.Demonstrate(context.Background(), "bank_download", &got); err != nil {
		t.Fatal(err)
	}
	want := []string{"new tab", control.ComputerRecordStart, "open " + fakeSites + "/bank/", "click Statement 2026-09", "click Download CSV",
		control.ComputerRecordStop, "GET /workflows?household_id=hh1", "GET /workflows?household_id=hh1", "PUT /workflows/tw_1", "close tab"}
	if !reflect.DeepEqual(f.log, want) {
		t.Fatalf("steps\n got %q\nwant %q", f.log, want)
	}
	if got.Recording != "rec_1" || got.Workflow != "tw_1" || got.Saved["name"] != "Download bank statement" {
		t.Fatalf("taught %+v", got)
	}
	wantPut := map[string]any{}
	for _, k := range []string{"name", "triggers", "params", "steps", "success", "shared"} {
		wantPut[k] = learnt[k]
	}
	if a, b := mustJSON(t, f.put), mustJSON(t, wantPut); a != b {
		t.Fatalf("saved\n got %s\nwant %s", a, b)
	}

	f.log = nil
	if err := tc.forget(context.Background(), got); err != nil {
		t.Fatal(err)
	}
	if want := []string{"DELETE /workflows/tw_1", "DELETE /recordings/rec_1"}; !reflect.DeepEqual(f.log, want) {
		t.Fatalf("forget %q, want %q", f.log, want)
	}
}

// TestDemonstrateStopsRecordingOnFailure: a demonstration that can't go on stops the recording
// (the agent would stay busy) and says what was missing.
func TestDemonstrateStopsRecordingOnFailure(t *testing.T) {
	f := &fakeBank{t: t, missing: "Statement 2026-09"}
	var got taught
	err := f.teacher(&fakeClock{t: time.Unix(0, 0)}).Demonstrate(context.Background(), "bank_download", &got)
	if err == nil || !strings.Contains(err.Error(), `step 2: no a "Statement 2026-09"`) {
		t.Fatalf("err = %v", err)
	}
	if n := len(f.log); n < 2 || f.log[n-2] != control.ComputerRecordStop || f.log[n-1] != "close tab" || got.Recording != "rec_1" {
		t.Fatalf("steps %q, taught %+v", f.log, got)
	}
	if err := (&teacher{}).Demonstrate(context.Background(), "bank_upload", &got); err == nil {
		t.Fatal("an unknown demonstration ran")
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
