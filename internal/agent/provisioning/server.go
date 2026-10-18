package provisioning

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// Options wires the server to the node. WiFi is required; a nil func skips its step.
type Options struct {
	WiFi WiFi
	// APSSID is the setup hotspot a failed attempt brings back so the app can retry; ""
	// disables that (no hotspot running, e.g. --wifi simulated).
	APSSID          string
	FirmwareVersion string
	Identity        func() Identity
	HTTPClient      *http.Client // for registration; nil is http.DefaultClient

	// SaveConfig keeps the request's room and URLs before the Wi-Fi join (config_write_failed).
	SaveConfig func(ProvisionRequest) error
	// SaveCreds stores what registration returned (credentials_save_failed).
	SaveCreds func(node.Creds) error
	// SaveK2 stores the settings key for the CC-assigned node id.
	SaveK2 func(nodeID, k2, kid, createdAt string) error
	// Provisioned says whether the node already finished provisioning: K2 is refused then, and
	// /info reports previously_provisioned.
	Provisioned     func() bool
	MarkProvisioned func() error
	// FactoryReset clears the node's state and lists what it cleared.
	FactoryReset func(context.Context) ([]string, error)
	// OnProvisioned runs a second after PROVISIONED (time for the app's last status poll).
	OnProvisioned func()
	Log           *slog.Logger
}

// Server is the provisioning HTTP API (N:provisioning/api.py), an http.Handler for :8080.
type Server struct {
	o     Options
	ctx   context.Context
	sm    *StateMachine
	mux   *http.ServeMux
	busy  sync.Mutex // one attempt at a time
	wg    sync.WaitGroup
	retry time.Duration // between registration tries
	grace time.Duration // PROVISIONED → OnProvisioned
}

// New builds the server; ctx bounds the background provisioning attempts.
func New(ctx context.Context, o Options) *Server {
	if o.HTTPClient == nil {
		o.HTTPClient = http.DefaultClient
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Identity == nil {
		o.Identity = func() Identity { return SystemIdentity("") }
	}
	s := &Server{o: o, ctx: ctx, sm: NewStateMachine(), mux: http.NewServeMux(), retry: 4 * time.Second, grace: time.Second}
	// The OS connectivity checks: without an answer, a phone may leave the hotspot (or route
	// around it) because it has "no internet".
	s.mux.HandleFunc("GET /generate_204", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	s.mux.HandleFunc("GET /hotspot-detect.html", plainText(appleSuccess))
	s.mux.HandleFunc("GET /library/test/success.html", plainText(appleSuccess))
	s.mux.HandleFunc("GET /success.txt", plainText("success"))
	s.mux.HandleFunc("GET /api/v1/info", s.info)
	s.mux.HandleFunc("GET /api/v1/scan-networks", s.scan)
	s.mux.HandleFunc("POST /api/v1/provision", s.provision)
	s.mux.HandleFunc("GET /api/v1/status", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, s.sm.Status()) })
	s.mux.HandleFunc("POST /api/v1/provision/k2", s.k2)
	s.mux.HandleFunc("POST /api/v1/factory-reset", s.factoryReset)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// StateMachine is the attempt's state, for a caller that must not touch the radio mid-attempt.
func (s *Server) StateMachine() *StateMachine { return s.sm }

const appleSuccess = "<HTML><HEAD><TITLE>Success</TITLE></HEAD><BODY>Success</BODY></HTML>"

func plainText(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, body)
	}
}

// CaptiveHandler is the port-80 listener (_CaptivePortalHandler): the phones' connectivity
// probes go to port 80, and anything it doesn't know gets 204.
func CaptiveHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		switch r.URL.Path {
		case "/hotspot-detect.html", "/library/test/success.html":
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, appleSuccess)
		case "/success.txt":
			w.Header().Set("Content-Type", "text/plain")
			io.WriteString(w, "success")
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
}

func (s *Server) provisioned() bool { return s.o.Provisioned != nil && s.o.Provisioned() }

func (s *Server) info(w http.ResponseWriter, _ *http.Request) {
	id := s.o.Identity()
	writeJSON(w, http.StatusOK, NodeInfo{
		NodeID: id.NodeID, FirmwareVersion: s.o.FirmwareVersion, Hardware: id.Hardware, MACAddress: id.MAC,
		Capabilities: []string{"voice", "speaker"}, State: s.sm.State(), PreviouslyProvisioned: s.provisioned(),
	})
}

func (s *Server) scan(w http.ResponseWriter, r *http.Request) {
	nets, err := s.o.WiFi.ScanNetworks(r.Context())
	if err != nil {
		s.o.Log.Error("provisioning: wifi scan", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"detail": "Wi-Fi scan failed: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, ScanNetworksResponse{Networks: nets})
}

func (s *Server) provision(w http.ResponseWriter, r *http.Request) {
	var req ProvisionRequest
	if !decode(w, r, &req, "wifi_ssid", "wifi_password", "room", "command_center_url", "household_id", "node_id", "provisioning_token") {
		return
	}
	if !s.busy.TryLock() {
		writeJSON(w, http.StatusOK, ProvisionResponse{Success: false, Message: msgBusy})
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.busy.Unlock()
		s.run(req)
	}()
	writeJSON(w, http.StatusOK, ProvisionResponse{Success: true, Message: msgAccepted})
}

// run is one attempt (_run_provisioning). Any failure after the node has left its hotspot is
// recovered from: this attempt's Wi-Fi profile goes, the hotspot comes back, and the status says
// ERROR with an error_code and whether the app can retry. Only success marks the node provisioned.
func (s *Server) run(req ProvisionRequest) {
	ctx := s.ctx
	leftHotspot := false
	fail := func(code ErrorCode, msg string, regStatus *int) {
		s.o.Log.Error("provisioning: attempt failed", "error_code", code, "error", msg)
		restored := false
		if leftHotspot {
			// Stay busy while recovering, so nothing else takes the radio meanwhile.
			s.sm.Transition(s.sm.State(), msgRestoring, -1)
			restored = s.restoreHotspot(ctx)
		}
		f := Failure{Error: msg, Code: code, RegistrationStatus: regStatus, Retryable: restored || !leftHotspot, HotspotRestored: restored}
		if restored {
			f.Message = msgFailedRestored
		}
		s.sm.SetError(f)
	}
	defer func() {
		if p := recover(); p != nil {
			fail(ErrInternal, fmt.Sprint(p), nil)
		}
	}()

	s.sm.Transition(StateConnecting, "Saving configuration...", 10)
	if s.o.SaveConfig != nil {
		if err := s.o.SaveConfig(req); err != nil {
			s.o.Log.Error("provisioning: save config", "err", err)
			fail(ErrConfigWrite, "Failed to update configuration", nil)
			return
		}
	}
	s.sm.Transition(StateConnecting, fmt.Sprintf("Saving credentials for %s...", req.WiFiSSID), 20)
	s.sm.Transition(StateConnecting, fmt.Sprintf("Connecting to %s...", req.WiFiSSID), 35)
	// From here on the hotspot may be gone, so every failure must bring it back.
	leftHotspot = true
	if err := s.o.WiFi.Connect(ctx, req.WiFiSSID, req.WiFiPassword); err != nil {
		s.o.Log.Error("provisioning: wifi connect", "ssid", req.WiFiSSID, "err", err)
		fail(ErrWiFiConnect, "Failed to connect to "+req.WiFiSSID, nil)
		return
	}
	s.sm.Transition(StateConnecting, "Connected to "+req.WiFiSSID, 50)

	s.sm.Transition(StateRegistering, "Registering with command center...", 70)
	reg := register(ctx, s.o.HTTPClient, req.CommandCenterURL, configURL(req), req.NodeID, req.ProvisioningToken, req.Room, s.retry)
	if !reg.ok() {
		var st *int
		if reg.status != 0 {
			st = &reg.status
		}
		s.o.Log.Error("provisioning: register", "kind", reg.kind, "status", reg.status, "detail", reg.detail)
		fail(ErrRegistrationFailed, reg.reason(), st)
		return
	}
	c := reg.creds
	if c.NodeID == "" {
		c.NodeID = req.NodeID
	}
	if c.Room == "" {
		c.Room = req.Room
	}
	if s.o.SaveCreds != nil {
		if err := s.o.SaveCreds(c); err != nil {
			s.o.Log.Error("provisioning: save credentials", "err", err)
			fail(ErrCredentialsSave, "Failed to save node credentials to config", nil)
			return
		}
	}
	s.sm.Transition(StateRegistering, "Finalizing...", 90)
	if err := s.o.WiFi.CommitConnection(ctx); err != nil {
		s.o.Log.Warn("provisioning: finalising the wifi profile", "err", err)
	}
	if s.o.MarkProvisioned != nil {
		if err := s.o.MarkProvisioned(); err != nil {
			fail(ErrInternal, err.Error(), nil)
			return
		}
	}
	s.sm.Transition(StateProvisioned, msgProvisioned, 100)
	if s.o.OnProvisioned != nil {
		select {
		case <-ctx.Done():
		case <-time.After(s.grace):
		}
		s.o.OnProvisioned()
	}
}

// restoreHotspot undoes this attempt's Wi-Fi join and starts the hotspot again; true when it is
// broadcasting.
func (s *Server) restoreHotspot(ctx context.Context) bool {
	if err := s.o.WiFi.RollbackConnection(ctx); err != nil {
		s.o.Log.Warn("provisioning: rolling back the wifi profile", "err", err)
	}
	if s.o.APSSID == "" {
		s.o.Log.Warn("provisioning: no setup hotspot ssid; cannot restore AP mode")
		return false
	}
	if err := s.o.WiFi.StartAPMode(ctx, s.o.APSSID); err != nil {
		s.o.Log.Error("provisioning: restarting the setup hotspot", "ssid", s.o.APSSID, "err", err)
		return false
	}
	s.o.Log.Info("provisioning: setup hotspot restored", "ssid", s.o.APSSID)
	return true
}

// configURL is the node's bootstrap address: the request's config service, else the command
// center's host on the config service's port (the app omits it only when it knows none).
func configURL(req ProvisionRequest) string {
	if req.ConfigServiceURL != "" {
		return req.ConfigServiceURL
	}
	u, err := url.Parse(req.CommandCenterURL)
	if err != nil || u.Host == "" {
		return req.CommandCenterURL
	}
	return u.Scheme + "://" + net.JoinHostPort(u.Hostname(), node.DefaultConfigPort)
}

func (s *Server) k2(w http.ResponseWriter, r *http.Request) {
	var req K2Request
	if !decode(w, r, &req, "node_id", "kid", "k2", "created_at") {
		return
	}
	if !validTime(req.CreatedAt) {
		validationError(w, "created_at", "Input should be a valid datetime")
		return
	}
	if s.provisioned() {
		writeJSON(w, http.StatusOK, K2Response{Error: ptrTo(msgK2Provisioned)})
		return
	}
	// node_id is the CC-assigned id the node is about to register as; the store keeps K2 for it.
	if s.o.SaveK2 == nil {
		writeJSON(w, http.StatusOK, K2Response{Error: ptrTo("this node cannot store K2")})
		return
	}
	if err := s.o.SaveK2(req.NodeID, req.K2, req.Kid, req.CreatedAt); err != nil {
		writeJSON(w, http.StatusOK, K2Response{Error: ptrTo(err.Error())})
		return
	}
	writeJSON(w, http.StatusOK, K2Response{Success: true, NodeID: &req.NodeID, Kid: &req.Kid})
}

func (s *Server) factoryReset(w http.ResponseWriter, r *http.Request) {
	if s.o.FactoryReset == nil {
		writeJSON(w, http.StatusOK, FactoryResetResponse{Error: ptrTo("factory reset is not available on this node yet")})
		return
	}
	cleared, err := s.o.FactoryReset(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, FactoryResetResponse{Error: ptrTo(err.Error())})
		return
	}
	s.sm.Reset()
	writeJSON(w, http.StatusOK, FactoryResetResponse{Success: true, Cleared: cleared})
}

func ptrTo[T any](v T) *T { return &v }

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(b)
}

// decode reads a JSON object into v, answering 422 as FastAPI does when the body isn't one,
// a required field is missing or null, or a field has the wrong type.
func decode(w http.ResponseWriter, r *http.Request, v any, required ...string) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		validationError(w, "", "Could not read the body")
		return false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		validationError(w, "", "JSON decode error")
		return false
	}
	for _, k := range required {
		if raw, ok := fields[k]; !ok || string(raw) == "null" {
			validationError(w, k, "Field required")
			return false
		}
	}
	if err := json.Unmarshal(body, v); err != nil {
		field := ""
		if te, ok := err.(*json.UnmarshalTypeError); ok {
			field = te.Field
		}
		validationError(w, field, "Input should be a valid string")
		return false
	}
	return true
}

func validationError(w http.ResponseWriter, field, msg string) {
	loc := []string{"body"}
	if field != "" {
		loc = append(loc, field)
	}
	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": []map[string]any{{"loc": loc, "msg": msg}}})
}

// validTime accepts what pydantic's datetime does from the app (ISO 8601, with or without a zone).
func validTime(s string) bool {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999"} {
		if _, err := time.Parse(layout, s); err == nil {
			return true
		}
	}
	return false
}
