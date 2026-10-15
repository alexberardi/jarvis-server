package firstrun

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// Join modes and states, as the page sees them.
const (
	ModePair   = "pair"
	ModeSignIn = "signin"

	JoinIdle            = ""
	JoinWaiting         = "waiting"          // a code is shown, nobody approved it yet
	JoinChooseHousehold = "choose_household" // signed in, belongs to several households
	JoinJoined          = "joined"
	JoinFailed          = "failed"
)

// joinState is the page's "Add this computer" step.
type joinState struct {
	Mode       string      `json:"mode,omitempty"`
	State      string      `json:"state"`
	Code       string      `json:"code,omitempty"`
	ExpiresIn  int         `json:"expires_in,omitempty"` // seconds left on the code
	Error      string      `json:"error,omitempty"`
	Households []household `json:"households,omitempty"`
	// Warning: joined, but setting up the rest (browser host, starting the agent) failed.
	Warning string `json:"warning,omitempty"`
}

type household struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// app is the page's state and handlers.
type app struct {
	s     *Server
	d     Deps
	find  *finder
	meter *meter

	mu       sync.Mutex
	server   *chosen
	join     joinState
	expires  time.Time
	cancel   context.CancelFunc // the pairing poll running now
	jwt      string             // signed in, choosing a household; memory only
	busy     bool               // a join is being finished
	wg       sync.WaitGroup
	joinedAs node.Creds
}

func newApp(s *Server) *app {
	a := &app{s: s, d: s.d, find: &finder{d: s.d, isLocal: isLocalURL}}
	if s.d.Mic != nil {
		a.meter = newMeter(s.d.Mic, s.d.Now)
	}
	return a
}

func (a *app) close() {
	a.mu.Lock()
	if a.cancel != nil {
		a.cancel()
	}
	a.jwt = ""
	a.mu.Unlock()
	a.wg.Wait()
	a.find.close()
	if a.meter != nil {
		a.meter.close()
	}
}

// --- state ---

type permissionView struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Why    string `json:"why"`
	Status string `json:"status"`
}

type portalView struct {
	Allowed   bool   `json:"allowed"`
	Available bool   `json:"available"`
	Why       string `json:"why,omitempty"`
}

type voiceView struct {
	On bool `json:"on"`
}

type stateView struct {
	Product     string           `json:"product"`
	Computer    string           `json:"computer"`
	OS          string           `json:"os"`
	Joined      bool             `json:"joined"`
	Server      *Found           `json:"server,omitempty"`
	Join        joinState        `json:"join"`
	Voice       *voiceView       `json:"voice,omitempty"`
	Mic         bool             `json:"mic"`
	Permissions []permissionView `json:"permissions,omitempty"`
	Portal      *portalView      `json:"portal,omitempty"`
	Stores      Stores           `json:"stores"`
	CanRemove   bool             `json:"can_remove"`
}

func (a *app) state(ctx context.Context) stateView {
	v := stateView{Product: "Jarvis Automation", Computer: a.d.ComputerName, OS: a.d.GOOS,
		Stores: a.d.Stores, Mic: a.meter != nil, CanRemove: a.d.Remove != nil}
	if c, ok := a.d.Creds(); ok {
		v.Joined = true
		name := c.Name
		if name == "" {
			name = a.d.ComputerName
		}
		v.Computer = name
		v.Server = &Found{URL: c.ConfigURL, Name: hostOf(c.ConfigURL)}
	}
	a.mu.Lock()
	v.Join = a.join
	if a.join.State == JoinWaiting {
		v.Join.ExpiresIn = max(0, int(a.expires.Sub(a.d.Now()).Seconds()))
	}
	if a.server != nil && !v.Joined {
		f := a.server.Found
		v.Server = &f
	}
	a.mu.Unlock()
	if v.Joined && v.Join.State == JoinIdle {
		v.Join.State = JoinJoined
	}
	if a.d.Voice != nil {
		v.Voice = &voiceView{On: a.d.Voice.On()}
	}
	for _, p := range a.d.Permissions {
		st := Unknown
		if p.Status != nil {
			st = p.Status()
		}
		v.Permissions = append(v.Permissions, permissionView{ID: p.ID, Label: p.Label, Why: p.Why, Status: st})
	}
	if a.d.Portal != nil {
		allowed, available, why := a.d.Portal.Status(ctx)
		v.Portal = &portalView{Allowed: allowed, Available: available, Why: why}
	}
	return v
}

func hostOf(raw string) string {
	if f, err := NormalizeAddress(raw); err == nil {
		raw = f
	}
	if u, err := url.Parse(raw); err == nil {
		return u.Hostname()
	}
	return raw
}

func (a *app) getState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.state(r.Context()))
}

// --- finding the server ---

func (a *app) getServers(w http.ResponseWriter, _ *http.Request) {
	list, searching := a.find.list()
	if list == nil {
		list = []Found{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"servers": list, "searching": searching})
}

func (a *app) postSearch(w http.ResponseWriter, r *http.Request) {
	if !readJSON(w, r, &struct{}{}) {
		return
	}
	a.find.search(a.s.ctx)
	a.getServers(w, r)
}

func (a *app) postServer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL  string `json:"url"`
		Name string `json:"name"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if _, ok := a.d.Creds(); ok {
		fail(w, http.StatusConflict, "This computer is already part of Jarvis Automation.")
		return
	}
	u, err := NormalizeAddress(req.URL)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	f := Found{URL: u, Name: hostOf(u), Where: WhereTyped}
	list, _ := a.find.list()
	if i := slices.IndexFunc(list, func(x Found) bool { return x.URL == u }); i >= 0 {
		f = list[i]
	}
	c, err := choose(r.Context(), a.d.HTTP, a.d.ConfigPort, f)
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	a.mu.Lock()
	if a.join.State == JoinWaiting || a.busy {
		a.mu.Unlock()
		fail(w, http.StatusConflict, "Cancel the code on screen first.")
		return
	}
	a.server, a.join, a.jwt = &c, joinState{}, ""
	a.mu.Unlock()
	a.getState(w, r)
}

func (a *app) postForget(w http.ResponseWriter, r *http.Request) {
	if !readJSON(w, r, &struct{}{}) {
		return
	}
	a.mu.Lock()
	if a.join.State == JoinWaiting || a.busy {
		a.mu.Unlock()
		fail(w, http.StatusConflict, "Cancel the code on screen first.")
		return
	}
	a.server, a.join, a.jwt = nil, joinState{}, ""
	a.mu.Unlock()
	a.getState(w, r)
}

// --- joining ---

// startJoin claims the join step for a new attempt, or answers why not.
func (a *app) startJoin(w http.ResponseWriter) (chosen, bool) {
	if _, ok := a.d.Creds(); ok {
		fail(w, http.StatusConflict, "This computer is already part of Jarvis Automation.")
		return chosen{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case a.server == nil:
		fail(w, http.StatusConflict, "Choose your Jarvis Automation server first.")
		return chosen{}, false
	case a.busy:
		fail(w, http.StatusConflict, "Hold on, this computer is being added.")
		return chosen{}, false
	}
	if a.cancel != nil {
		a.cancel() // a code shown before: replaced
		a.cancel = nil
	}
	a.jwt = ""
	return *a.server, true
}

func (a *app) postPair(w http.ResponseWriter, r *http.Request) {
	if !readJSON(w, r, &struct{}{}) {
		return
	}
	srv, ok := a.startJoin(w)
	if !ok {
		return
	}
	p, err := node.StartPairing(r.Context(), a.d.HTTP, srv.cc, a.d.ComputerName, a.d.GOOS)
	if err != nil {
		a.failJoin(ModePair, err)
		fail(w, http.StatusBadGateway, friendly(err))
		return
	}
	ctx, cancel := context.WithCancel(a.s.ctx)
	a.mu.Lock()
	a.join = joinState{Mode: ModePair, State: JoinWaiting, Code: p.Code}
	a.expires = a.d.Now().Add(time.Duration(p.ExpiresIn) * time.Second)
	a.cancel = cancel
	a.wg.Add(1)
	a.mu.Unlock()
	go func() {
		defer a.wg.Done()
		defer cancel()
		res, err := node.WaitPairing(ctx, a.d.HTTP, srv.cc, p, a.d.WaitPairing)
		if ctx.Err() != nil {
			return // cancelled, or replaced by a newer attempt
		}
		if err != nil {
			a.failJoin(ModePair, err)
			return
		}
		name := res.Name
		if name == "" {
			name = a.d.ComputerName
		}
		// The approver chose the room: register without one.
		a.finishJoin(ctx, ModePair, srv, res.ProvisioningToken, "", name)
	}()
	a.getState(w, r)
}

func (a *app) postPairCancel(w http.ResponseWriter, r *http.Request) {
	if !readJSON(w, r, &struct{}{}) {
		return
	}
	a.mu.Lock()
	if a.cancel != nil && !a.busy {
		a.cancel()
		a.cancel = nil
		a.join = joinState{}
	}
	a.mu.Unlock()
	a.getState(w, r)
}

// signInRoom is where a computer added by signing in goes (enroll's default).
const signInRoom = "desktop"

func (a *app) postSignIn(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Email == "" || req.Password == "" {
		fail(w, http.StatusBadRequest, "Type your email and password.")
		return
	}
	srv, ok := a.startJoin(w)
	if !ok {
		return
	}
	jwt, err := node.Login(r.Context(), a.d.HTTP, srv.auth, req.Email, req.Password)
	req.Password = ""
	if err != nil {
		a.failJoin(ModeSignIn, err)
		fail(w, http.StatusUnauthorized, friendlyLogin(err))
		return
	}
	hs, err := node.Households(r.Context(), a.d.HTTP, srv.auth, jwt)
	if err != nil {
		a.failJoin(ModeSignIn, err)
		fail(w, http.StatusBadGateway, friendly(err))
		return
	}
	switch len(hs) {
	case 0:
		err := errors.New("Your account isn't in a household yet. Ask whoever set up Jarvis Automation to invite you.")
		a.mu.Lock()
		a.join = joinState{Mode: ModeSignIn, State: JoinFailed, Error: err.Error()}
		a.mu.Unlock()
		fail(w, http.StatusConflict, err.Error())
		return
	case 1:
		a.mintAndJoin(w, r, srv, jwt, hs[0].ID)
		return
	}
	view := make([]household, 0, len(hs))
	for _, h := range hs {
		view = append(view, household{ID: h.ID, Name: h.Name})
	}
	a.mu.Lock()
	a.jwt = jwt
	a.join = joinState{Mode: ModeSignIn, State: JoinChooseHousehold, Households: view}
	a.mu.Unlock()
	a.getState(w, r)
}

func (a *app) postHousehold(w http.ResponseWriter, r *http.Request) {
	var req struct {
		HouseholdID string `json:"household_id"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	a.mu.Lock()
	jwt, srv, st := a.jwt, a.server, a.join
	a.mu.Unlock()
	if jwt == "" || srv == nil || st.State != JoinChooseHousehold {
		fail(w, http.StatusConflict, "Sign in again first.")
		return
	}
	if !slices.ContainsFunc(st.Households, func(h household) bool { return h.ID == req.HouseholdID }) {
		fail(w, http.StatusBadRequest, "Pick one of your households.")
		return
	}
	a.mintAndJoin(w, r, *srv, jwt, req.HouseholdID)
}

// mintAndJoin is the rest of `enroll --email`: a provisioning token for the household, then
// registering. The session token is dropped either way.
func (a *app) mintAndJoin(w http.ResponseWriter, r *http.Request, srv chosen, jwt, householdID string) {
	a.mu.Lock()
	a.jwt = ""
	a.mu.Unlock()
	tok, err := node.MintToken(r.Context(), a.d.HTTP, srv.cc, jwt, householdID, a.d.ComputerName, signInRoom)
	if err != nil {
		a.failJoin(ModeSignIn, err)
		fail(w, http.StatusBadGateway, friendly(err))
		return
	}
	if !a.finishJoin(r.Context(), ModeSignIn, srv, tok, signInRoom, a.d.ComputerName) {
		a.mu.Lock()
		msg := a.join.Error
		a.mu.Unlock()
		fail(w, http.StatusBadGateway, msg)
		return
	}
	a.getState(w, r)
}

// finishJoin registers with a provisioning token and keeps the sign-in, then sets up the rest.
func (a *app) finishJoin(ctx context.Context, mode string, srv chosen, tok node.ProvisioningToken, room, name string) bool {
	a.mu.Lock()
	if a.busy {
		a.mu.Unlock()
		return false
	}
	a.busy = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.busy = false
		a.mu.Unlock()
	}()
	c, err := node.RegisterPreferring(ctx, a.d.HTTP, tok.NodeCommandCenterURL, srv.cc, srv.URL, tok.NodeID, tok.Token, room,
		func(err error) { a.d.Log.Info("setup page: registering through discovery's URL", "unreachable", err) })
	if err == nil {
		c.Name = name
		if a.d.SaveCreds == nil {
			err = errors.New("nowhere to save the sign-in")
		} else {
			err = a.d.SaveCreds(c)
		}
	}
	if err != nil {
		a.failJoin(mode, err)
		return false
	}
	a.d.Log.Info("setup page: this computer joined", "node", c.NodeID, "room", c.Room, "mode", mode)
	a.mu.Lock()
	a.join, a.joinedAs, a.cancel = joinState{Mode: mode, State: JoinJoined}, c, nil
	a.mu.Unlock()
	if a.d.AfterJoin != nil {
		if err := a.d.AfterJoin(ctx); err != nil {
			a.d.Log.Warn("setup page: after joining", "err", err)
			a.mu.Lock()
			a.join.Warning = "This computer was added, but the agent couldn't start: " + err.Error()
			a.mu.Unlock()
		}
	}
	return true
}

func (a *app) failJoin(mode string, err error) {
	msg := friendly(err)
	if mode == ModeSignIn {
		msg = friendlyLogin(err)
	}
	a.mu.Lock()
	a.join = joinState{Mode: mode, State: JoinFailed, Error: msg}
	a.cancel = nil
	a.mu.Unlock()
}

// friendlyLogin is friendly, plus what a refused password means.
func friendlyLogin(err error) string {
	var he *node.HTTPError
	if errors.As(err, &he) && (he.Status == http.StatusUnauthorized || he.Status == http.StatusBadRequest) {
		return "That email and password don't match. Check them and try again."
	}
	return friendly(err)
}

// friendly says what went wrong in plain words.
func friendly(err error) string {
	var he *node.HTTPError
	switch {
	case errors.Is(err, node.ErrPasswordChangeRequired):
		return "This account still has a temporary password. Change your password first: sign in to " +
			"Jarvis Automation in the app or on the web and choose a new one. Then sign in here again."
	case errors.Is(err, node.ErrPairingDenied):
		return "Someone said no to this computer. You can show a new code and try again."
	case errors.Is(err, node.ErrPairingExpired):
		return "The code ran out before anyone approved it. Show a new code and try again."
	case errors.As(err, &he) && he.Status == http.StatusTooManyRequests:
		return "Too many tries for now. Wait a few minutes and try again."
	case errors.As(err, &he) && he.Status == http.StatusForbidden:
		return "Your account can't add computers to that household. Ask its admin."
	case errors.As(err, &he):
		return "The server said no (" + he.Error() + "). Try again, or ask whoever runs Jarvis Automation."
	}
	return "Couldn't reach the server. Check that it's on and try again."
}

// --- voice, microphone, permissions, portal ---

func (a *app) postVoice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		On bool `json:"on"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if a.d.Voice == nil {
		fail(w, http.StatusNotFound, "Listening for \"Hey Jarvis\" isn't available on this computer yet.")
		return
	}
	if err := a.d.Voice.Set(req.On); err != nil {
		fail(w, http.StatusInternalServerError, "Couldn't change that: "+err.Error())
		return
	}
	a.getState(w, r)
}

func (a *app) getMic(w http.ResponseWriter, _ *http.Request) {
	if a.meter == nil {
		fail(w, http.StatusNotFound, "There's no microphone check on this computer yet.")
		return
	}
	level, errText := a.meter.Level()
	writeJSON(w, http.StatusOK, map[string]any{"level": level, "error": errText})
}

func (a *app) postPermission(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	i := slices.IndexFunc(a.d.Permissions, func(p Permission) bool { return p.ID == req.ID })
	if i < 0 {
		fail(w, http.StatusNotFound, "Unknown permission.")
		return
	}
	p := a.d.Permissions[i]
	st := Unknown
	if p.Status != nil {
		st = p.Status()
	}
	if st != Granted && st != Denied && p.Ask != nil {
		// macOS's own prompt (it may wait for the answer, so not on this request).
		a.wg.Add(1)
		go func() { defer a.wg.Done(); p.Ask() }()
		writeJSON(w, http.StatusOK, map[string]string{"did": "asked"})
		return
	}
	if a.d.OpenURL == nil || p.Pane == "" {
		fail(w, http.StatusNotFound, "Open System Settings, then Privacy & Security, then "+p.Label+".")
		return
	}
	if err := a.d.OpenURL(r.Context(), p.Pane); err != nil {
		fail(w, http.StatusInternalServerError, "Couldn't open System Settings. Open it, then Privacy & Security, then "+p.Label+".")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"did": "opened"})
}

// portalWait bounds how long the page waits for the desktop's dialog.
const portalWait = 3 * time.Minute

func (a *app) postPortal(w http.ResponseWriter, r *http.Request) {
	if !readJSON(w, r, &struct{}{}) {
		return
	}
	if a.d.Portal == nil {
		fail(w, http.StatusNotFound, "This desktop doesn't need that step.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), portalWait)
	defer cancel()
	if err := a.d.Portal.Allow(ctx); err != nil {
		fail(w, http.StatusConflict, "Typing wasn't allowed ("+err.Error()+"). You can try again.")
		return
	}
	a.getState(w, r)
}

// --- done, remove ---

func (a *app) postDone(w http.ResponseWriter, r *http.Request) {
	if !readJSON(w, r, &struct{}{}) {
		return
	}
	a.s.finish("done")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *app) postRemove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Confirm string `json:"confirm"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if a.d.Remove == nil {
		fail(w, http.StatusNotFound, "Removing isn't available here.")
		return
	}
	if req.Confirm != "remove" {
		fail(w, http.StatusBadRequest, "Confirm first.")
		return
	}
	a.mu.Lock()
	if a.cancel != nil {
		a.cancel()
		a.cancel = nil
	}
	a.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Minute)
	defer cancel()
	if err := a.d.Remove(ctx); err != nil {
		a.d.Log.Warn("setup page: remove", "err", err)
		fail(w, http.StatusInternalServerError, "Some parts couldn't be removed: "+err.Error())
		return
	}
	a.s.finish("removed")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "note": a.d.RemoveNote})
}

// decodeSmall reads a small JSON answer.
func decodeSmall(resp *http.Response, v any) error {
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(v)
}
