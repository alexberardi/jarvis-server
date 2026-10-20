package cc

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/errands"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/modules/notifications"
)

// Statements and spreadsheets in errands (plan P3 piece 13, "do my budget"): read_computer_file
// reads a statement a computer_task step downloaded (the agent's computer_files read), and
// write_spreadsheet saves an .xlsx in the computer's ~/Documents/Jarvis (computer_files write)
// and attaches it for the errand's completion card. Both are errand steps only (the planner's
// menu, prompts.ServerToolAllowed), on a computer with computer control on: the one a
// computer_task step uses unless the plan names another.

const (
	toolReadComputerFile = "read_computer_file"
	toolWriteSpreadsheet = "write_spreadsheet"

	// maxFileText is how much of a file's text read_computer_file hands back (the agent sends
	// up to 1 MiB); it is kept in the step's result and read again by the replan.
	maxFileText = 32 << 10
	// maxSpreadsheetBytes is the agent's computer_files write limit.
	maxSpreadsheetBytes = 5 << 20
	// spreadsheetOwner owns the spreadsheets attached to errand cards.
	spreadsheetOwner = errands.AttachmentOwner
	spreadsheetDir   = "~/Documents/Jarvis/"
)

// serverAttacher stores a file jarvisd made for an inbox item (the notifications module's
// UploadServerAttachment).
type serverAttacher interface {
	UploadServerAttachment(ctx context.Context, owner, householdID, contentType string, r io.Reader) (notifications.Attachment, error)
}

// fileComputer is the computer a file step uses: the one named, when it has computer control
// on, else the first with it on (where computer_task runs).
func (m *Module) fileComputer(ctx context.Context, turn servertools.Turn, named string) (string, *pyjson.Object, error) {
	cc, err := m.computerControl(ctx, turn.HouseholdID)
	if err != nil {
		return "", nil, err
	}
	ids := cc.targets()
	if len(ids) == 0 {
		return "", recordingRefusal("computer_off", cc.offMessage()), nil
	}
	if genericComputerNames[computerKey(named)] {
		return ids[0], nil, nil
	}
	p, err := m.pickComputer(ctx, turn, named, anyComputer, nil)
	if err != nil || p.refusal != nil {
		return "", p.refusal, err
	}
	if !slices.Contains(ids, p.target.row.nodeID) {
		return "", recordingRefusal("computer_off", "Computer control is off on "+p.target.label()+": turn it on in the Jarvis Automation menu there."), nil
	}
	return p.target.row.nodeID, nil, nil
}

type readComputerFileTool struct{ m *Module }

func (*readComputerFileTool) Name() string { return toolReadComputerFile }

func (*readComputerFileTool) Definition() *pyjson.Object {
	return watchToolDefinition(toolReadComputerFile,
		"Read a text file on the user's computer, such as a bank or card statement a computer_task downloaded "+
			"(CSV, OFX, TXT or JSON, up to 1 MiB). Returns its text.",
		servertools.Obj("path", strProp("The file's path on the computer, e.g. ~/Downloads/statement-2026-09.csv"),
			"computer", computerProp()), "path")
}

func (t *readComputerFileTool) Execute(ctx context.Context, call servertools.Call, turn servertools.Turn) (any, error) {
	m := t.m
	if e := toolSpeakerError(turn); e != nil {
		return e, nil
	}
	p := argText(call, "path")
	if p == "" {
		return recordingRefusal("invalid_argument", "Which file? I need its path on the computer."), nil
	}
	nodeID, refusal, err := m.fileComputer(ctx, turn, argText(call, "computer"))
	if err != nil || refusal != nil {
		return refusal, err
	}
	out := m.agentRecordingCall(ctx, nodeID, computeruse.ToolFiles, map[string]any{"op": "read", "path": p}, turn.Speaker.UserID)
	if !dispatchOK(out) {
		return recordingRefusal(agentCode(out, "read_failed"), "I couldn't read "+p+": "+agentError(out)), nil
	}
	if real, _ := out["path"].(string); real != "" {
		p = real
	}
	text, ok := out["text"].(string)
	if !ok {
		return recordingRefusal("not_text", "I can't read "+path.Base(p)+": I read only CSV, OFX, TXT and JSON files of 1 MiB or less."), nil
	}
	cut := len(text) > maxFileText
	if cut {
		text = text[:maxFileText]
		if i := strings.LastIndexByte(text, '\n'); i > 0 {
			text = text[:i+1]
		}
		text = strings.ToValidUTF8(text, "")
	}
	msg := fmt.Sprintf("Read %s (%d bytes).", path.Base(p), len(text))
	if cut {
		msg = fmt.Sprintf("Read the first %d bytes of %s.", len(text), path.Base(p))
	}
	return servertools.Obj("path", p, "bytes", out["bytes"], "text", text, "truncated", cut, "message", msg), nil
}

type writeSpreadsheetTool struct{ m *Module }

func (*writeSpreadsheetTool) Name() string { return toolWriteSpreadsheet }

func (*writeSpreadsheetTool) Definition() *pyjson.Object {
	sheet := servertools.Obj("type", "object", "properties", servertools.Obj(
		"title", strProp("The sheet's name, e.g. Transactions"),
		"columns", servertools.Obj("type", "array", "items", servertools.Obj("type", "string"), "description", "The header row"),
		"rows", servertools.Obj("type", "array", "items", servertools.Obj("type", "array"),
			"description", "One list of cells per row, in column order; amounts as numbers")))
	return watchToolDefinition(toolWriteSpreadsheet,
		"Save a spreadsheet (.xlsx) in the Jarvis folder of the user's Documents on their computer and attach it "+
			"to the errand's report, e.g. a budget: the categorised transactions and a total per category.",
		servertools.Obj("name", strProp("The file name without .xlsx, e.g. Budget 2026-09"),
			"sheets", servertools.Obj("type", "array", "items", sheet,
				"description", "[{title, columns: [...], rows: [[...], ...]}], one per sheet"),
			"computer", computerProp()), "name", "sheets")
}

func (t *writeSpreadsheetTool) Execute(ctx context.Context, call servertools.Call, turn servertools.Turn) (any, error) {
	m := t.m
	if e := toolSpeakerError(turn); e != nil {
		return e, nil
	}
	name, err := servertools.SpreadsheetName(argText(call, "name"))
	if err != nil {
		return recordingRefusal("invalid_argument", "I can't save the spreadsheet: "+err.Error()+"."), nil
	}
	raw, _ := call.Arg("sheets")
	sheets, err := servertools.SheetsFromArgs(raw)
	if err != nil {
		return recordingRefusal("invalid_argument", "I can't build the spreadsheet: "+err.Error()+"."), nil
	}
	data, err := servertools.BuildWorkbook(sheets)
	if err != nil {
		return nil, err
	}
	if len(data) > maxSpreadsheetBytes {
		return recordingRefusal("too_large", "The spreadsheet is too large to save (over 5 MiB): use fewer rows."), nil
	}
	nodeID, refusal, err := m.fileComputer(ctx, turn, argText(call, "computer"))
	if err != nil || refusal != nil {
		return refusal, err
	}
	dest := spreadsheetDir + name + ".xlsx"
	out := m.agentRecordingCall(ctx, nodeID, computeruse.ToolFiles, map[string]any{"op": "write", "path": dest,
		"content_b64": base64.StdEncoding.EncodeToString(data)}, turn.Speaker.UserID)
	if !dispatchOK(out) {
		return recordingRefusal(agentCode(out, "write_failed"), "I couldn't save the spreadsheet: "+agentError(out)), nil
	}
	if real, _ := out["path"].(string); real != "" {
		dest = real
	}
	rows := 0
	for _, s := range sheets {
		rows += len(s.Rows)
	}
	res := servertools.Obj("status", "saved", "path", dest, "bytes", len(data), "sheets", len(sheets), "rows", rows,
		"message", fmt.Sprintf("Saved the spreadsheet as %s (%d sheets, %d rows).", dest, len(sheets), rows))
	if up, ok := m.Notify.(serverAttacher); ok {
		a, err := up.UploadServerAttachment(ctx, spreadsheetOwner, turn.HouseholdID, notifications.XLSXMime, bytes.NewReader(data))
		if err != nil {
			m.deps.Log.Warn("cc: couldn't attach a spreadsheet", "path", dest, "err", err)
		} else {
			res.Set("attachment_ids", []any{a.ID})
		}
	}
	return res, nil
}
