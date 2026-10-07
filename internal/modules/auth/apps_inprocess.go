package auth

import (
	"context"
	"errors"
)

// App clients in process, for the admin Connections page (AD7) and the legacy
// /admin/app-clients routes (admin.go), which call these. A key is returned once, at create or
// rotate; only its hash is stored.

// AppClient is an app client without its key.
type AppClient struct {
	AppID         string  `json:"app_id"`
	Name          string  `json:"name"`
	IsActive      bool    `json:"is_active"`
	CreatedAt     string  `json:"created_at"`
	LastRotatedAt *string `json:"last_rotated_at"`
}

var (
	// ErrAppExists is an app_id already taken.
	ErrAppExists = errors.New("auth: app_id already exists")
	// ErrAppNotFound is an unknown app_id.
	ErrAppNotFound = errors.New("auth: app client not found")
)

func (a *appClient) public() AppClient {
	out := AppClient{AppID: a.appID, Name: a.name, IsActive: a.isActive, CreatedAt: pyTime(a.createdAt).(string)}
	if s, ok := pyTimeNull(a.lastRotatedAt).(string); ok {
		out.LastRotatedAt = &s
	}
	return out
}

// AppClients lists every app client, oldest first.
func (m *Module) AppClients(ctx context.Context) ([]AppClient, error) {
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT `+appCols+` FROM auth_app_clients ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AppClient{}
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a.public())
	}
	return out, rows.Err()
}

// CreateAppClient creates an active client and returns its key (shown once). ErrAppExists
// when the id is taken.
func (m *Module) CreateAppClient(ctx context.Context, appID, name string) (AppClient, string, error) {
	key := tokenURLSafe(48)
	hash, err := hashSecret(key)
	if err != nil {
		return AppClient{}, "", err
	}
	t := dbTime(now())
	_, err = m.deps.DB.Write.ExecContext(ctx, `INSERT INTO auth_app_clients
		(app_id, name, key_hash, is_active, created_at, updated_at) VALUES (?, ?, ?, 1, ?, ?)`, appID, name, hash, t, t)
	if isUnique(err) {
		return AppClient{}, "", ErrAppExists
	}
	if err != nil {
		return AppClient{}, "", err
	}
	return AppClient{AppID: appID, Name: name, IsActive: true, CreatedAt: pyTime(t).(string)}, key, nil
}

// RotateAppClient issues a new key (shown once) and reactivates the client (the one explicit
// "reissue + reactivate" action, STATUS 2026-10-06). It returns the rotation time.
func (m *Module) RotateAppClient(ctx context.Context, appID string) (key, rotatedAt string, err error) {
	key = tokenURLSafe(48)
	hash, err := hashSecret(key)
	if err != nil {
		return "", "", err
	}
	t := now()
	res, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE auth_app_clients
		SET key_hash = ?, last_rotated_at = ?, is_active = 1, updated_at = ? WHERE app_id = ?`, hash, dbTime(t), dbTime(t), appID)
	if err != nil {
		return "", "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", "", ErrAppNotFound
	}
	m.verified.invalidate("app", appID)
	return key, pyTimeOf(t), nil
}

// RevokeAppClient deactivates a client; its key stops validating at once.
func (m *Module) RevokeAppClient(ctx context.Context, appID string) error {
	res, err := m.deps.DB.Write.ExecContext(ctx,
		`UPDATE auth_app_clients SET is_active = 0, updated_at = ? WHERE app_id = ?`, dbTime(now()), appID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrAppNotFound
	}
	m.verified.invalidate("app", appID)
	return nil
}
