package models

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/engine"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
)

// Model states.
const (
	StateDownloading = "downloading"
	StateReady       = "ready"
	StateFailed      = "failed"
)

// Install states.
const (
	InstallQueued    = "queued"
	InstallRunning   = "running"
	InstallDone      = "done"
	InstallFailed    = "failed"
	InstallCancelled = "cancelled"
)

// File is one file of an installed model.
type File struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256,omitempty"`
}

// Model is an installed (or installing) model.
type Model struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	Display        string `json:"display"`
	CatalogID      string `json:"catalog_id,omitempty"`
	Repo           string `json:"repo,omitempty"`
	Revision       string `json:"revision,omitempty"`
	SourceURL      string `json:"source_url,omitempty"`
	Archive        string `json:"archive,omitempty"`
	Files          []File `json:"files"`
	Path           string `json:"path"`
	Size           int64  `json:"size"`
	MMProjID       string `json:"mmproj_id,omitempty"`
	ContextDefault int    `json:"context_default,omitempty"`
	PromptProvider string `json:"prompt_provider,omitempty"`
	State          string `json:"state"`
	BytesDone      int64  `json:"bytes_done"`
	Error          string `json:"error,omitempty"`
	External       bool   `json:"external"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

// Install is one install request's progress.
type Install struct {
	ID            int64    `json:"id"`
	ModelID       string   `json:"model_id"`
	MMProjID      string   `json:"mmproj_id,omitempty"`
	EngineKind    string   `json:"engine_kind,omitempty"`
	EngineFlavour string   `json:"engine_flavour,omitempty"`
	Assign        []string `json:"assign"`
	State         string   `json:"state"`
	Phase         string   `json:"phase"`
	BytesTotal    int64    `json:"bytes_total"`
	BytesDone     int64    `json:"bytes_done"`
	Error         string   `json:"error,omitempty"`
	Note          string   `json:"note,omitempty"`
	JobID         int64    `json:"job_id,omitempty"`
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`
}

// Store is the llm_models / llm_installs tables. It implements engine.ModelLookup.
type Store struct{ DB *db.DB }

// ErrNotFound is returned for an unknown model or install.
var ErrNotFound = errors.New("not found")

const modelCols = `id, kind, display, COALESCE(catalog_id,''), COALESCE(repo,''), COALESCE(revision,''), COALESCE(source_url,''), COALESCE(archive,''), files, path, size,
	COALESCE(mmproj_id,''), context_default, COALESCE(prompt_provider,''), state, bytes_done, COALESCE(error,''), external,
	created_at, updated_at`

type scanner interface{ Scan(...any) error }

func scanModel(s scanner) (Model, error) {
	var m Model
	var files string
	err := s.Scan(&m.ID, &m.Kind, &m.Display, &m.CatalogID, &m.Repo, &m.Revision, &m.SourceURL, &m.Archive, &files, &m.Path, &m.Size,
		&m.MMProjID, &m.ContextDefault, &m.PromptProvider, &m.State, &m.BytesDone, &m.Error, &m.External,
		&m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return m, err
	}
	_ = json.Unmarshal([]byte(files), &m.Files)
	return m, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Get returns a model by id.
func (s *Store) Get(ctx context.Context, id string) (Model, error) {
	m, err := scanModel(s.DB.Read.QueryRowContext(ctx, `SELECT `+modelCols+` FROM llm_models WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}

// List returns every model, by kind then id.
func (s *Store) List(ctx context.Context) ([]Model, error) {
	rows, err := s.DB.Read.QueryContext(ctx, `SELECT `+modelCols+` FROM llm_models ORDER BY kind, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Model{}
	for rows.Next() {
		m, err := scanModel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Upsert writes a model row (insert or full replace).
func (s *Store) Upsert(ctx context.Context, m Model) error {
	files, _ := json.Marshal(m.Files)
	_, err := s.DB.Write.ExecContext(ctx, `
		INSERT INTO llm_models (id, kind, display, catalog_id, repo, revision, source_url, archive, files, path, size, mmproj_id,
		                        context_default, prompt_provider, state, bytes_done, error, external)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET kind = excluded.kind, display = excluded.display, catalog_id = excluded.catalog_id,
			repo = excluded.repo, revision = excluded.revision, source_url = excluded.source_url,
			archive = excluded.archive, files = excluded.files, path = excluded.path,
			size = excluded.size, mmproj_id = excluded.mmproj_id, context_default = excluded.context_default,
			prompt_provider = excluded.prompt_provider, state = excluded.state, bytes_done = excluded.bytes_done,
			error = excluded.error, external = excluded.external,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')`,
		m.ID, m.Kind, m.Display, nullable(m.CatalogID), nullable(m.Repo), nullable(m.Revision), nullable(m.SourceURL),
		nullable(m.Archive), string(files), m.Path, m.Size,
		nullable(m.MMProjID), m.ContextDefault, nullable(m.PromptProvider), m.State, m.BytesDone, nullable(m.Error), m.External)
	return err
}

// SetModelState records a model's state, progress and error.
func (s *Store) SetModelState(ctx context.Context, id, state string, done int64, errMsg string) error {
	_, err := s.DB.Write.ExecContext(ctx, `
		UPDATE llm_models SET state = ?, bytes_done = ?, error = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		WHERE id = ?`, state, done, nullable(errMsg), id)
	return err
}

// Delete removes a model row.
func (s *Store) Delete(ctx context.Context, id string) error {
	_, err := s.DB.Write.ExecContext(ctx, `DELETE FROM llm_models WHERE id = ?`, id)
	return err
}

// LookupModel implements engine.ModelLookup: a ready model whose file is on disk.
func (s *Store) LookupModel(ctx context.Context, id string) (engine.ModelInfo, error) {
	m, err := s.Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return engine.ModelInfo{}, engine.ErrModelNotFound
	}
	if err != nil {
		return engine.ModelInfo{}, err
	}
	if m.State != StateReady {
		return engine.ModelInfo{}, fmt.Errorf("%w (state %s)", engine.ErrModelNotFound, m.State)
	}
	if _, err := os.Stat(m.Path); err != nil {
		return engine.ModelInfo{}, fmt.Errorf("model file missing: %w", err)
	}
	info := engine.ModelInfo{ID: m.ID, Kind: m.Kind, Path: m.Path, MMProjID: m.MMProjID, ContextDefault: m.ContextDefault}
	if e, ok := CatalogEntry(m.CatalogID); ok && m.Kind == engine.ModelLLM {
		// The catalog's pinned template and fold flag (ID12) describe exactly this file: a
		// catalog install is verified against the entry's sha256.
		if info.ChatTemplate, err = e.Template(); err != nil {
			return engine.ModelInfo{}, err
		}
		info.FoldSystemMessages = e.FoldSystemMessages
	}
	return info, nil
}

const installCols = `id, model_id, COALESCE(mmproj_id,''), COALESCE(engine_kind,''), COALESCE(engine_flavour,''), assign, state,
	phase, bytes_total, bytes_done, COALESCE(error,''), COALESCE(note,''), COALESCE(job_id,0), created_at, updated_at`

func scanInstall(s scanner) (Install, error) {
	var i Install
	var assign string
	err := s.Scan(&i.ID, &i.ModelID, &i.MMProjID, &i.EngineKind, &i.EngineFlavour, &assign, &i.State, &i.Phase,
		&i.BytesTotal, &i.BytesDone, &i.Error, &i.Note, &i.JobID, &i.CreatedAt, &i.UpdatedAt)
	if err != nil {
		return i, err
	}
	_ = json.Unmarshal([]byte(assign), &i.Assign)
	if i.Assign == nil {
		i.Assign = []string{}
	}
	return i, nil
}

// CreateInstall inserts an install and returns its id.
func (s *Store) CreateInstall(ctx context.Context, i Install) (int64, error) {
	assign, _ := json.Marshal(i.Assign)
	var id int64
	err := s.DB.Write.QueryRowContext(ctx, `
		INSERT INTO llm_installs (model_id, mmproj_id, engine_kind, engine_flavour, assign, state, phase, bytes_total, note)
		VALUES (?, ?, ?, ?, ?, ?, '', ?, ?) RETURNING id`,
		i.ModelID, nullable(i.MMProjID), nullable(i.EngineKind), nullable(i.EngineFlavour), string(assign), InstallQueued,
		i.BytesTotal, nullable(i.Note)).Scan(&id)
	return id, err
}

// GetInstall returns an install.
func (s *Store) GetInstall(ctx context.Context, id int64) (Install, error) {
	i, err := scanInstall(s.DB.Read.QueryRowContext(ctx, `SELECT `+installCols+` FROM llm_installs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return i, ErrNotFound
	}
	return i, err
}

// ListInstalls returns installs, newest first.
func (s *Store) ListInstalls(ctx context.Context, limit int) ([]Install, error) {
	rows, err := s.DB.Read.QueryContext(ctx, `SELECT `+installCols+` FROM llm_installs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Install{}
	for rows.Next() {
		i, err := scanInstall(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// ActiveInstall returns the queued or running install of a model, if any.
func (s *Store) ActiveInstall(ctx context.Context, modelID string) (Install, bool, error) {
	i, err := scanInstall(s.DB.Read.QueryRowContext(ctx, `SELECT `+installCols+` FROM llm_installs
		WHERE (model_id = ? OR mmproj_id = ?) AND state IN ('queued', 'running') ORDER BY id DESC LIMIT 1`, modelID, modelID))
	if errors.Is(err, sql.ErrNoRows) {
		return i, false, nil
	}
	return i, err == nil, err
}

// UpdateInstall writes an install's progress fields.
func (s *Store) UpdateInstall(ctx context.Context, i Install) error {
	return s.updateInstall(ctx, s.DB.Write, i)
}

// updateInstall is UpdateInstall on ex: the store's writer, or a transaction.
func (s *Store) updateInstall(ctx context.Context, ex interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, i Install) error {
	_, err := ex.ExecContext(ctx, `
		UPDATE llm_installs SET state = ?, phase = ?, bytes_total = ?, bytes_done = ?, error = ?, note = ?, job_id = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		WHERE id = ?`, i.State, i.Phase, i.BytesTotal, i.BytesDone, nullable(i.Error), nullable(i.Note), i.JobID, i.ID)
	return err
}

// SetInstallJob records the queue job of a new install, unless a worker already recorded one.
func (s *Store) SetInstallJob(ctx context.Context, id, jobID int64) error {
	_, err := s.DB.Write.ExecContext(ctx, `
		UPDATE llm_installs SET job_id = ? WHERE id = ? AND COALESCE(job_id, 0) = 0`, jobID, id)
	return err
}

// SetInstallState changes only an install's state (and error), unless it is already final.
func (s *Store) SetInstallState(ctx context.Context, id int64, state, errMsg string) (bool, error) {
	res, err := s.DB.Write.ExecContext(ctx, `
		UPDATE llm_installs SET state = ?, error = COALESCE(?, error), updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		WHERE id = ? AND state IN ('queued', 'running')`, state, nullable(errMsg), id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

var slugBad = regexp.MustCompile(`[^a-z0-9._-]+`)

// Slug makes an id from a file or repo name.
func Slug(s string) string {
	s = strings.ToLower(s)
	for _, ext := range []string{".gguf", ".bin"} {
		s = strings.TrimSuffix(s, ext)
	}
	s = slugBad.ReplaceAllString(s, "-")
	return strings.Trim(s, "-.")
}

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

// ValidID reports whether s can be a model id.
func ValidID(s string) bool { return idRe.MatchString(s) && !strings.Contains(s, "..") }
