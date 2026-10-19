package main

import "net/http"

// control is the suite's API:
//
//	GET  /control/state               {orders, downloads, forms, sessions}
//	POST /control/reset               forget all of it (and the carts)
//	POST /control/login?site=S        S = store|bank|card: a new session; Set-Cookie and
//	                                  {"site", "cookie": {"name", "value", "path"}} for the suite to
//	                                  inject into the browser profile, standing in for a saved login
func (s *Server) control(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/control/state" && r.Method == http.MethodGet:
		s.mu.Lock()
		st := s.st
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, st)
	case r.URL.Path == "/control/reset" && r.Method == http.MethodPost:
		s.mu.Lock()
		s.st = emptyState()
		s.carts = map[string][]CartItem{}
		s.save()
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]string{"status": "reset"})
	case r.URL.Path == "/control/login" && r.Method == http.MethodPost:
		site := r.URL.Query().Get("site")
		if _, ok := walled[site]; !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "site must be store, bank or card"})
			return
		}
		c := s.newSession(w, site)
		writeJSON(w, http.StatusOK, map[string]any{
			"site":   site,
			"cookie": map[string]string{"name": c.Name, "value": c.Value, "path": c.Path},
		})
	default:
		http.NotFound(w, r)
	}
}
