package firstrun

import "net/http"

func (a *app) getSettings(w http.ResponseWriter, r *http.Request) {
	if a.d.Settings == nil {
		fail(w, 503, "Settings are unavailable on this agent.")
		return
	}
	v, err := a.d.Settings(r.Context(), nil)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, v)
}
func (a *app) postSettings(w http.ResponseWriter, r *http.Request) {
	if a.d.Settings == nil {
		fail(w, 503, "Settings are unavailable on this agent.")
		return
	}
	var body struct {
		Settings map[string]any `json:"settings"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if len(body.Settings) == 0 {
		fail(w, 400, "Choose at least one setting.")
		return
	}
	v, err := a.d.Settings(r.Context(), body.Settings)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, v)
}
func (a *app) postRestart(w http.ResponseWriter, r *http.Request) {
	if !readJSON(w, r, &struct{}{}) {
		return
	}
	if a.d.Restart == nil {
		fail(w, 503, "Restart is unavailable.")
		return
	}
	if err := a.d.Restart(r.Context()); err != nil {
		fail(w, 500, "Jarvis could not restart. Try opening it again.")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
