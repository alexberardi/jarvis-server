package cc

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// The admin Store page (decision 2026-10-30-admin-store-page) browses the household's Pantry
// through jarvisd rather than from the browser: the household's own Pantry URL and its
// pantry.enabled switch apply, and the browser needs no CORS or CSP exception. The install
// itself is the ordinary per-node package-install request.

func (m *Module) registerPantryStore(mux *http.ServeMux) {
	const base = "/api/v0/households/{household_id}/pantry/commands"
	mux.HandleFunc("GET "+base, m.user(m.handlePantryBrowse))
	mux.HandleFunc("GET "+base+"/{command_name}", m.user(m.handlePantryCommand))
}

// pantryStoreQuery is what the browse passes on to Pantry's GET /v1/commands.
var pantryStoreQuery = []string{"q", "category", "sort", "page", "per_page"}

// pantryStoreGate checks the caller is a member and the household has the Pantry on, and
// resolves the store to browse: ?pantry_store_id= when the client picked one (only a store in
// the household's list, decision 14), else the default.
func (m *Module) pantryStoreGate(w http.ResponseWriter, r *http.Request, u authn.User) (PantryStore, bool) {
	hh := r.PathValue("household_id")
	if err := m.requireRole(r.Context(), u.ID, hh, authn.RoleMember); err != nil {
		m.writeErr(w, err)
		return PantryStore{}, false
	}
	if !m.requirePantry(w, r, hh) {
		return PantryStore{}, false
	}
	store, err := m.pantryStore(r.Context(), hh, r.URL.Query().Get("pantry_store_id"))
	if err != nil {
		m.writeErr(w, err)
		return PantryStore{}, false
	}
	return store, true
}

func (m *Module) pantryStoreUnavailable(w http.ResponseWriter, store PantryStore, err error) {
	m.deps.Log.Warn("cc: Pantry store read failed", "store", store.ID, "err", err)
	detailCode(w, http.StatusServiceUnavailable, "The Pantry store "+store.Name+" can't be reached right now. Try again later.",
		pantryUnavailableCode)
}

// handlePantryBrowse is Pantry's package list, as Pantry returns it.
func (m *Module) handlePantryBrowse(w http.ResponseWriter, r *http.Request, u authn.User) {
	store, ok := m.pantryStoreGate(w, r, u)
	if !ok {
		return
	}
	q := url.Values{}
	for _, k := range pantryStoreQuery {
		if v := r.URL.Query().Get(k); v != "" {
			q.Set(k, v)
		}
	}
	path := "/v1/commands"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out map[string]any
	if err := m.pantryGet(r.Context(), store.base(), path, &out); err != nil {
		m.pantryStoreUnavailable(w, store, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// handlePantryCommand is Pantry's detail for one package (the Store's package page). With
// ?install=1 it adds git_tag, the commit of its latest version (what the Store installs), or null
// when Pantry has none. The commit comes from /download, as the app's does: a version's git_tag
// in /versions need not exist in the repository. /download counts an install, so only the
// install dialog asks for it, not a look at the package.
func (m *Module) handlePantryCommand(w http.ResponseWriter, r *http.Request, u authn.User) {
	store, ok := m.pantryStoreGate(w, r, u)
	if !ok {
		return
	}
	ctx, name := r.Context(), url.PathEscape(r.PathValue("command_name"))
	var d map[string]any
	switch err := m.pantryGet(ctx, store.base(), "/v1/commands/"+name, &d); {
	case errors.Is(err, errPantryNoPackage):
		detailCode(w, http.StatusNotFound, "The Pantry store "+store.Name+" has no package "+r.PathValue("command_name"), packageNotFoundCode)
		return
	case err != nil:
		m.pantryStoreUnavailable(w, store, err)
		return
	}
	if r.URL.Query().Get("install") != "1" {
		httpx.WriteJSON(w, http.StatusOK, d)
		return
	}
	var dl struct {
		Commit string `json:"git_commit_sha"`
	}
	if err := m.pantryGet(ctx, store.base(), "/v1/commands/"+name+"/download", &dl); err != nil && !errors.Is(err, errPantryNoPackage) {
		m.pantryStoreUnavailable(w, store, err)
		return
	}
	d["git_tag"] = nil
	if isCommitSHA(dl.Commit) {
		d["git_tag"] = dl.Commit
	}
	httpx.WriteJSON(w, http.StatusOK, d)
}
