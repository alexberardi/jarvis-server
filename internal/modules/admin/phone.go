package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/sip"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// The Phone page's SIP support routes (docs/cc/phone-sip.md §6): every SIP account's status,
// the self-tests and the SIP trace. All superuser: install-wide, and the trace carries every
// household's call signalling (numbers dialed, caller IDs).

// PhoneSIP is the SIP provider's support surface (the cc module).
type PhoneSIP interface {
	SIPAccounts() []phone.SIPAccountView
	StartSIPTest(ctx context.Context, port int, test string, in phone.SIPTestInput) (phone.SIPTestRun, error)
	SIPTrace() []sip.TraceDialog
}

type sipStatusJSON struct {
	Accounts     []phone.SIPAccountView `json:"accounts"`
	Tests        []string               `json:"tests"`
	FeatureCodes []string               `json:"feature_codes"`
}

// handleSIPStatus is GET /api/phone/sip: each account's registration, OPTIONS round trip,
// active calls and last self-tests (the page polls it while a test runs).
func (m *Module) handleSIPStatus(w http.ResponseWriter, _ *http.Request) {
	if m.Phone == nil {
		unavailable(w, "cc")
		return
	}
	accts := m.Phone.SIPAccounts()
	if accts == nil {
		accts = []phone.SIPAccountView{}
	}
	httpx.WriteJSON(w, http.StatusOK, sipStatusJSON{Accounts: accts, Tests: phone.SIPTests, FeatureCodes: sip.FeatureCodes})
}

// handleSIPTest is POST /api/phone/sip/{port}/tests/{test}, body {"number", "household_id"}
// for call_me (the confirm card goes to the caller, in the account's household, else in
// household_id, else the token's current one). 202 with the run's first state.
func (m *Module) handleSIPTest(w http.ResponseWriter, r *http.Request) {
	if m.Phone == nil {
		unavailable(w, "cc")
		return
	}
	port, err := strconv.Atoi(r.PathValue("port"))
	if err != nil || port < 1 || port > 65535 {
		httpx.Error(w, http.StatusNotFound, "No SIP account on that port")
		return
	}
	var body struct {
		Number      string `json:"number"`
		HouseholdID string `json:"household_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		httpx.Error(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	u, err := m.verify(r.Context(), tok) // the gate let it through: it verifies
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "Invalid or expired token")
		return
	}
	hh := strings.TrimSpace(body.HouseholdID)
	if hh == "" {
		hh = u.HouseholdID
	}
	run, err := m.Phone.StartSIPTest(r.Context(), port, r.PathValue("test"),
		phone.SIPTestInput{Number: body.Number, HouseholdID: hh, UserID: u.ID})
	var input *phone.InputError
	switch {
	case err == nil:
		m.deps.Log.Info("admin: SIP self-test started", "port", port, "test", run.Test, "user", u.ID)
		httpx.WriteJSON(w, http.StatusAccepted, run)
	case errors.Is(err, phone.ErrSIPAccountNotFound), errors.Is(err, phone.ErrSIPTestUnknown):
		httpx.Error(w, http.StatusNotFound, err.Error())
	case errors.Is(err, phone.ErrSIPTestBusy):
		httpx.Error(w, http.StatusConflict, err.Error())
	case errors.As(err, &input):
		httpx.Error(w, http.StatusBadRequest, input.Msg)
	default:
		m.deps.Log.Error("admin: SIP self-test failed to start", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "Internal server error")
	}
}

// handleSIPTrace is GET /api/phone/sip/trace: the last calls' and registrations' SIP
// messages, redacted. ?format=text is the export: a plain-text download for a support request.
func (m *Module) handleSIPTrace(w http.ResponseWriter, r *http.Request) {
	if m.Phone == nil {
		unavailable(w, "cc")
		return
	}
	dialogs := m.Phone.SIPTrace()
	if r.URL.Query().Get("format") == "text" {
		name := "jarvis-sip-trace-" + time.Now().UTC().Format("20060102-150405") + ".txt"
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		sip.WriteTraceText(w, dialogs)
		return
	}
	if dialogs == nil {
		dialogs = []sip.TraceDialog{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"dialogs": dialogs})
}
