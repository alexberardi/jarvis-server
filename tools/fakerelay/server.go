package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// Server is the fake push relay: jarvisd's relay protocol (internal/modules/notifications
// push.go), recording every push instead of handing it to Expo, and a control API for tests.
//
//	POST /v1/register        {"household_id"} → {"jwt"}
//	POST /v1/send            Bearer <the household's jwt>, X-Household-Id;
//	                         {"tokens", "title", "body", "data", "priority"} → {"results": [{status, error?, token}]}
//	                         A token starting with "fail:" answers DeviceNotRegistered.
//	GET  /control/pushes     {"pushes": [...]} oldest first; ?token=, ?household_id=, ?title= filter
//	DELETE /control/pushes   forget them
//	POST /control/fail       {"status": 503, "count": 1}: the next count sends answer that status
type Server struct {
	Log io.Writer // every recorded push, one JSON object per line; nil = none

	mu     sync.Mutex
	jwts   map[string]string // household → its jwt
	pushes []Push
	seq    int
	failN  int
	failAs int
}

// Push is one /v1/send the relay accepted.
type Push struct {
	Seq         int            `json:"seq"`
	Time        time.Time      `json:"time"`
	HouseholdID string         `json:"household_id"`
	Tokens      []string       `json:"tokens"`
	Title       string         `json:"title"`
	Body        string         `json:"body"`
	Data        map[string]any `json:"data"`
	Priority    string         `json:"priority"`
}

type result struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	Token  string `json:"token"`
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/health":
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	case r.URL.Path == "/v1/register" && r.Method == http.MethodPost:
		s.register(w, r)
	case r.URL.Path == "/v1/send" && r.Method == http.MethodPost:
		s.send(w, r)
	case r.URL.Path == "/control/pushes" && r.Method == http.MethodGet:
		s.list(w, r)
	case r.URL.Path == "/control/pushes" && r.Method == http.MethodDelete:
		s.mu.Lock()
		s.pushes = nil
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"pushes": []Push{}})
	case r.URL.Path == "/control/fail" && r.Method == http.MethodPost:
		var in struct{ Status, Count int }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Status < 400 || in.Count < 0 {
			writeError(w, http.StatusBadRequest, `want {"status": 4xx|5xx, "count": N}`)
			return
		}
		s.mu.Lock()
		s.failAs, s.failN = in.Status, in.Count
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]int{"status": in.Status, "count": in.Count})
	default:
		writeError(w, http.StatusNotFound, "not found: "+r.Method+" "+r.URL.Path)
	}
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var in struct {
		HouseholdID string `json:"household_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.HouseholdID == "" {
		writeError(w, http.StatusBadRequest, "household_id is required")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.jwts == nil {
		s.jwts = map[string]string{}
	}
	jwt, ok := s.jwts[in.HouseholdID]
	if !ok {
		// Random per process: a restarted relay refuses the JWTs jarvisd cached, as a real
		// relay with a new key would, and jarvisd registers again.
		b := make([]byte, 8)
		_, _ = rand.Read(b)
		jwt = "fakerelay." + in.HouseholdID + "." + hex.EncodeToString(b)
		s.jwts[in.HouseholdID] = jwt
	}
	writeJSON(w, http.StatusOK, map[string]string{"jwt": jwt})
}

func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	hh := r.Header.Get("X-Household-Id")
	jwt, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	var in struct {
		Tokens   []string       `json:"tokens"`
		Title    string         `json:"title"`
		Body     string         `json:"body"`
		Data     map[string]any `json:"data"`
		Priority string         `json:"priority"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "bad JSON: "+err.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if hh == "" || jwt == "" || s.jwts[hh] != jwt {
		writeError(w, http.StatusUnauthorized, "no valid JWT for this household")
		return
	}
	if s.failN > 0 {
		s.failN--
		writeError(w, s.failAs, "failing on purpose (/control/fail)")
		return
	}
	s.seq++
	p := Push{Seq: s.seq, Time: time.Now().UTC(), HouseholdID: hh, Tokens: in.Tokens, Title: in.Title, Body: in.Body,
		Data: in.Data, Priority: in.Priority}
	if p.Tokens == nil {
		p.Tokens = []string{}
	}
	s.pushes = append(s.pushes, p)
	if s.Log != nil {
		b, _ := json.Marshal(p)
		_, _ = s.Log.Write(append(b, '\n'))
	}
	out := make([]result, len(in.Tokens))
	for i, t := range in.Tokens {
		out[i] = result{Status: "ok", Token: t}
		if strings.HasPrefix(t, "fail:") {
			out[i] = result{Status: "error", Error: "DeviceNotRegistered", Token: t}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": out})
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.mu.Lock()
	out := []Push{}
	for _, p := range s.pushes {
		if t := q.Get("token"); t != "" && !slices.Contains(p.Tokens, t) {
			continue
		}
		if h := q.Get("household_id"); h != "" && p.HouseholdID != h {
			continue
		}
		if t := q.Get("title"); t != "" && p.Title != t {
			continue
		}
		out = append(out, p)
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"pushes": out})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"detail": msg})
}
