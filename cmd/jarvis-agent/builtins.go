package main

import (
	"context"
	"log/slog"
	"sync/atomic"

	"github.com/alexberardi/jarvis-server/internal/agent/builtins"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// connectCC keeps live set to jarvisd's client while the runner is connected (the built-ins,
// approvals and recording uploads use it). Call it after everything else that sets OnConnected.
func connectCC(r *node.Runner, live *atomic.Pointer[node.CC]) {
	serve := r.OnConnected
	r.OnConnected = func(ctx context.Context, cc *node.CC) {
		live.Store(cc)
		defer live.CompareAndSwap(cc, nil)
		if serve != nil {
			serve(ctx, cc)
		} else {
			<-ctx.Done()
		}
	}
}

// wireRoutineVerb answers jarvisd's `routine` (the app's Run now and a routine's schedule) as
// N:'s handle_routine: the routine tool runs jarvisd's definition (D24) on tools, and its
// output goes to the device-control result sink.
func wireRoutineVerb(r *node.Runner, tools node.Toolset, log *slog.Logger) {
	addVerbs(r, map[string]func(context.Context, *node.CC, map[string]any){
		"routine": func(ctx context.Context, cc *node.CC, det map[string]any) {
			rid, _ := det["reply_request_id"].(string)
			if rid == "" {
				log.Warn("routine: no reply_request_id; ignored")
				return
			}
			verifyID, _ := det["request_id"].(string)
			if verifyID == "" {
				verifyID = rid
			}
			if ok, err := cc.Verify(ctx, verifyID); err != nil || !ok {
				log.Warn("routine failed verification; dropped", "request_id", verifyID, "err", err)
				return
			}
			slug, _ := det["routine_name"].(string)
			if slug == "" {
				_ = cc.PostToolResult(ctx, rid, node.Fail("Missing routine_name"))
				return
			}
			args := map[string]any{"routine_name": slug}
			if def, ok := det["routine"].(map[string]any); ok {
				args["routine_definition"] = def
			}
			req := node.Request{}
			req.ToolCallID, _ = det["tool_call_id"].(string)
			req.VoiceCommand, _ = det["voice_command"].(string)
			cctx, cancel := context.WithTimeout(ctx, builtins.RoutineTimeout)
			out, handled := tools.Call(cctx, "routine", args, req)
			cancel()
			if !handled {
				out = node.Fail("The routine command is turned off on this node")
			}
			node.TakeSignals(out)
			log.Info("routine", "routine", slug, "success", out["success"], "passed", out["passed"], "failed", out["failed"])
			if err := cc.PostToolResult(ctx, rid, out); err != nil {
				log.Warn("routine: result not posted", "request_id", rid, "err", err)
			}
		},
	})
}
