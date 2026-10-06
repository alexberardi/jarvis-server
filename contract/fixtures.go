//go:build contract

package contract

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"
)

// Fixtures are created through the target's real public and admin APIs, tagged with the run id,
// and removed afterwards:
//
//   - App: POST /admin/app-clients; cleanup revokes it. jarvis-auth has no delete for app
//     clients, so revoked "contract-<run>" rows accumulate on the target. Harmless on a dev box.
//   - User: POST /auth/register, which also creates the user's solo "My Home" household;
//     cleanup is DELETE /auth/me, which deletes the user and cascades the solo household and
//     its nodes (it also fans out a purge to command-center and notifications).
//   - Node: POST /admin/nodes in a user's household; cleanup deactivates it (a 404 is fine:
//     the user's cleanup may already have cascaded it).
//
// Shared fixtures are created once per run on first use and torn down by TestMain. Tests that
// mutate a fixture (deactivate a node, rotate a refresh token) make their own with New*.

// App is an app-to-app client.
type App struct {
	ID  string
	Key string
	// owned is false when the app came from JARVIS_CONTRACT_APP_ID/KEY; it is then not revoked.
	owned bool
}

// H is the app-to-app header pair.
func (a *App) H() H { return H{"X-Jarvis-App-Id": a.ID, "X-Jarvis-App-Key": a.Key} }

// User is a throwaway user with their own household.
type User struct {
	ID           int
	Email        string
	Password     string
	AccessToken  string
	RefreshToken string
	HouseholdID  string
	Superuser    bool
}

// H is the user's Bearer header.
func (u *User) H() H { return Bearer(u.AccessToken) }

// Node is a node registered in jarvis-auth.
type Node struct {
	ID          string
	Key         string
	HouseholdID string
	Services    []string
}

// APIKeyH is the command-center style node header (X-API-Key: node_id:node_key).
func (n *Node) APIKeyH() H { return H{"X-API-Key": n.ID + ":" + n.Key} }

// LogsH is the jarvis-logs / log-client node header pair.
func (n *Node) LogsH() H { return H{"X-Node-Id": n.ID, "X-Node-Key": n.Key} }

// --- error-returning operations (usable outside a *testing.T) ---

func (tg *Target) createApp(name string) (*App, error) {
	id := fmt.Sprintf("contract-%s-%s", tg.RunID, name)
	r, err := tg.do(Auth, http.MethodPost, "/admin/app-clients",
		map[string]string{"app_id": id, "name": "contract suite " + name}, tg.AdminH())
	if err != nil {
		return nil, err
	}
	var out struct {
		Key string `json:"key"`
	}
	if err := r.expect(http.StatusCreated, &out); err != nil {
		return nil, err
	}
	return &App{ID: id, Key: out.Key, owned: true}, nil
}

func (tg *Target) revokeApp(a *App) error {
	if !a.owned {
		return nil
	}
	r, err := tg.do(Auth, http.MethodPost, "/admin/app-clients/"+a.ID+"/revoke", nil, tg.AdminH())
	if err != nil {
		return err
	}
	return r.expect(http.StatusOK, nil)
}

var userSeq struct {
	sync.Mutex
	n int
}

func (tg *Target) registerUser() (*User, error) {
	userSeq.Lock()
	userSeq.n++
	n := userSeq.n
	userSeq.Unlock()
	u := &User{
		Email:    fmt.Sprintf("contract-%s-%d@example.com", tg.RunID, n),
		Password: "contract-" + randHex(8),
	}
	r, err := tg.do(Auth, http.MethodPost, "/auth/register",
		map[string]string{"email": u.Email, "password": u.Password}, nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		HouseholdID  string `json:"household_id"`
		User         struct {
			ID int `json:"id"`
		} `json:"user"`
	}
	if err := r.expect(http.StatusCreated, &out); err != nil {
		return nil, err
	}
	u.ID, u.AccessToken, u.RefreshToken, u.HouseholdID = out.User.ID, out.AccessToken, out.RefreshToken, out.HouseholdID
	return u, nil
}

// login refreshes u's tokens with a password login.
func (tg *Target) login(u *User) error {
	r, err := tg.do(Auth, http.MethodPost, "/auth/login",
		map[string]string{"email": u.Email, "password": u.Password}, nil)
	if err != nil {
		return err
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := r.expect(http.StatusOK, &out); err != nil {
		return err
	}
	u.AccessToken, u.RefreshToken = out.AccessToken, out.RefreshToken
	return nil
}

func (tg *Target) setSuperuser(u *User, on bool) error {
	r, err := tg.do(Auth, http.MethodPut, fmt.Sprintf("/admin/users/%d/superuser", u.ID),
		map[string]bool{"is_superuser": on}, tg.AdminH())
	if err != nil {
		return err
	}
	if err := r.expect(http.StatusOK, nil); err != nil {
		return err
	}
	u.Superuser = on
	if !on {
		return nil
	}
	// The is_superuser claim is minted at login, so get a token that carries it.
	return tg.login(u)
}

func (tg *Target) deleteUser(u *User) error {
	del := func() (*Resp, error) {
		return tg.do(Auth, http.MethodDelete, "/auth/me", map[string]string{"password": u.Password}, u.H())
	}
	r, err := del()
	if err != nil {
		return err
	}
	if r.Status == http.StatusUnauthorized {
		// The access token expired during a long run (or was minted before a demotion).
		if err := tg.login(u); err != nil {
			return fmt.Errorf("login before delete: %w", err)
		}
		if r, err = del(); err != nil {
			return err
		}
	}
	return r.expect(http.StatusNoContent, nil)
}

func (tg *Target) createNode(householdID string, services ...string) (*Node, error) {
	id := fmt.Sprintf("contract-%s-node-%s", tg.RunID, randHex(3))
	if services == nil {
		services = []string{}
	}
	r, err := tg.do(Auth, http.MethodPost, "/admin/nodes", map[string]any{
		"node_id": id, "household_id": householdID, "name": "contract node", "services": services,
	}, tg.AdminH())
	if err != nil {
		return nil, err
	}
	var out struct {
		NodeKey string `json:"node_key"`
	}
	if err := r.expect(http.StatusCreated, &out); err != nil {
		return nil, err
	}
	return &Node{ID: id, Key: out.NodeKey, HouseholdID: householdID, Services: services}, nil
}

func (tg *Target) deactivateNode(n *Node) error {
	r, err := tg.do(Auth, http.MethodDelete, "/admin/nodes/"+n.ID, nil, tg.AdminH())
	if err != nil {
		return err
	}
	if r.Status == http.StatusNotFound {
		return nil
	}
	return r.expect(http.StatusOK, nil)
}

// expect checks the status and optionally decodes the JSON body, without a *testing.T.
func (r *Resp) expect(status int, into any) error {
	if r.Status != status {
		return fmt.Errorf("want %d: %s", status, r.describe())
	}
	if into != nil {
		if err := json.Unmarshal(r.Body, into); err != nil {
			return fmt.Errorf("decode: %v: %s", err, r.describe())
		}
	}
	return nil
}

// --- per-test fixtures (cleaned up with t.Cleanup) ---

// NewApp mints a throwaway app client, revoked when the test ends.
func NewApp(t testing.TB, name string) *App {
	t.Helper()
	tg := T(t)
	tg.NeedAdmin(t)
	a, err := tg.createApp(name)
	if err != nil {
		t.Fatalf("fixture app: %v", err)
	}
	t.Cleanup(func() {
		if err := tg.revokeApp(a); err != nil {
			t.Errorf("cleanup app %s: %v", a.ID, err)
		}
	})
	return a
}

// NewUser registers a throwaway user (and household), deleted when the test ends.
func NewUser(t testing.TB) *User {
	t.Helper()
	tg := T(t)
	tg.NeedAdmin(t)
	u, err := tg.registerUser()
	if err != nil {
		t.Fatalf("fixture user: %v", err)
	}
	t.Cleanup(func() {
		if err := tg.deleteUser(u); err != nil {
			t.Errorf("cleanup user %s: %v", u.Email, err)
		}
	})
	return u
}

// NewNode registers a throwaway node in householdID with access to services, deactivated when
// the test ends.
func NewNode(t testing.TB, householdID string, services ...string) *Node {
	t.Helper()
	tg := T(t)
	tg.NeedAdmin(t)
	n, err := tg.createNode(householdID, services...)
	if err != nil {
		t.Fatalf("fixture node: %v", err)
	}
	t.Cleanup(func() {
		if err := tg.deactivateNode(n); err != nil {
			t.Errorf("cleanup node %s: %v", n.ID, err)
		}
	})
	return n
}

// --- shared fixtures (created once, torn down by TestMain) ---

var shared struct {
	mu       sync.Mutex
	app      *App
	user     *User
	super    *User
	node     *Node
	cleanups []func() error
}

func addSharedCleanup(f func() error) { shared.cleanups = append(shared.cleanups, f) }

// runSharedCleanups tears shared fixtures down in reverse creation order.
func runSharedCleanups() {
	shared.mu.Lock()
	defer shared.mu.Unlock()
	for i := len(shared.cleanups) - 1; i >= 0; i-- {
		if err := shared.cleanups[i](); err != nil {
			fmt.Fprintf(os.Stderr, "contract: shared fixture cleanup: %v\n", err)
		}
	}
	shared.cleanups = nil
}

// SharedApp is the run's app client: JARVIS_CONTRACT_APP_ID/KEY when set, otherwise a minted one.
func SharedApp(t testing.TB) *App {
	t.Helper()
	tg := T(t)
	tg.Need(t, Auth)
	shared.mu.Lock()
	defer shared.mu.Unlock()
	if shared.app != nil {
		return shared.app
	}
	if tg.AppID != "" && tg.AppKey != "" {
		shared.app = &App{ID: tg.AppID, Key: tg.AppKey}
		return shared.app
	}
	tg.NeedAdmin(t)
	a, err := tg.createApp("shared")
	if err != nil {
		t.Fatalf("shared app: %v", err)
	}
	shared.app = a
	addSharedCleanup(func() error { return tg.revokeApp(a) })
	return a
}

// SharedUser is the run's ordinary (non-superuser) user. Don't mutate it.
func SharedUser(t testing.TB) *User {
	t.Helper()
	tg := T(t)
	tg.NeedAdmin(t)
	shared.mu.Lock()
	defer shared.mu.Unlock()
	if shared.user != nil {
		return shared.user
	}
	u, err := tg.registerUser()
	if err != nil {
		t.Fatalf("shared user: %v", err)
	}
	shared.user = u
	addSharedCleanup(func() error { return tg.deleteUser(u) })
	return u
}

// SharedSuperuser is the run's superuser, needed for the superuser-only settings reads.
func SharedSuperuser(t testing.TB) *User {
	t.Helper()
	tg := T(t)
	tg.NeedAdmin(t)
	shared.mu.Lock()
	defer shared.mu.Unlock()
	if shared.super != nil {
		return shared.super
	}
	u, err := tg.registerUser()
	if err != nil {
		t.Fatalf("shared superuser: %v", err)
	}
	addSharedCleanup(func() error {
		// Demote first so a failed delete never leaves a stray superuser behind.
		derr := tg.setSuperuser(u, false)
		if err := tg.deleteUser(u); err != nil {
			return err
		}
		return derr
	})
	if err := tg.setSuperuser(u, true); err != nil {
		t.Fatalf("shared superuser promote: %v", err)
	}
	shared.super = u
	return u
}

// SharedNode is a node in SharedUser's household with access to the shared app's service id
// and to jarvis-logs. Don't mutate it.
func SharedNode(t testing.TB) *Node {
	t.Helper()
	app := SharedApp(t)
	u := SharedUser(t)
	tg := T(t)
	shared.mu.Lock()
	defer shared.mu.Unlock()
	if shared.node != nil {
		return shared.node
	}
	n, err := tg.createNode(u.HouseholdID, app.ID, "jarvis-logs")
	if err != nil {
		t.Fatalf("shared node: %v", err)
	}
	shared.node = n
	addSharedCleanup(func() error { return tg.deactivateNode(n) })
	return n
}
