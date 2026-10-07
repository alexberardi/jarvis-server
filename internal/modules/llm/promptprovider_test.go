package llm

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/models"
	"github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// A fresh install has no llm.prompt_provider; CC asks for the live model's (from its
// catalog entry), so the first voice turn works.
func TestLivePromptProvider(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	d, err := db.Open(ctx, filepath.Join(home, "jarvis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := db.Migrate(ctx, d, queue.MigrationModule, queue.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, d, "llm", Migrations()); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := &Module{Auth: fakeAuth{}, Version: "test"}
	m.Register(http.NewServeMux(), module.Deps{Config: config.Config{Home: home}, DB: d, Log: log, Queue: queue.New(d, log)})

	if err := m.settings.Migrate(ctx); err != nil { // Start does this; no engines here
		t.Fatal(err)
	}
	if p := m.LivePromptProvider(ctx); p != "" {
		t.Fatalf("no live model: %q", p)
	}
	store := m.stack.Manager.Store
	e, _ := models.CatalogEntry("qwen3-8b")
	if err := store.Upsert(ctx, models.Model{ID: "qwen3-8b", Kind: "llm", Display: "q", CatalogID: e.ID, Path: "/x", State: models.StateReady}); err != nil {
		t.Fatal(err)
	}
	if err := m.settings.Set(ctx, "llm.live.model", "qwen3-8b", settings.Scope{}); err != nil {
		t.Fatal(err)
	}
	if p := m.LivePromptProvider(ctx); p == "" || p != e.PromptProvider {
		t.Fatalf("catalog provider: %q want %q", p, e.PromptProvider)
	}
	// The installed row's own provider wins (set at install, or by hand for a registered file).
	if err := store.Upsert(ctx, models.Model{ID: "mine", Kind: "llm", Display: "m", Path: "/y", PromptProvider: "Qwen3_14B_Compressed", State: models.StateReady}); err != nil {
		t.Fatal(err)
	}
	if err := m.settings.Set(ctx, "llm.live.model", "mine", settings.Scope{}); err != nil {
		t.Fatal(err)
	}
	if p := m.LivePromptProvider(ctx); p != "Qwen3_14B_Compressed" {
		t.Fatalf("row provider: %q", p)
	}
}

// The admin setup state reads label states and the hardware summary in process (A3).
func TestSetupSummary(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	d, err := db.Open(ctx, filepath.Join(home, "jarvis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := db.Migrate(ctx, d, queue.MigrationModule, queue.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, d, "llm", Migrations()); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := &Module{Auth: fakeAuth{}, Version: "test"}
	if m.LabelStates(ctx) != nil {
		t.Fatal("label states before Register")
	}
	m.Register(http.NewServeMux(), module.Deps{Config: config.Config{Home: home}, DB: d, Log: log, Queue: queue.New(d, log)})
	if err := m.settings.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if m.Settings() == nil {
		t.Fatal("no settings accessor")
	}

	states := m.LabelStates(ctx)
	for _, l := range []string{"live", "background", "stt", "tts", "speaker"} {
		if states[l] != StateNotConfigured {
			t.Errorf("%s = %q on a fresh install, want not_configured (all: %v)", l, states[l], states)
		}
	}
	if _, ok := states["embeddings"]; !ok {
		t.Errorf("no embeddings state: %v", states)
	}
	hw, ok := m.HardwareSummary(ctx)
	if !ok || hw.Hardware.Flavour == "" || hw.Proposal == nil {
		t.Fatalf("hardware summary: %+v %v", hw, ok)
	}
	for _, k := range []string{"llama-server", "whisper-server"} {
		if _, ok := hw.Flavours[k]; !ok {
			t.Errorf("no %s flavours: %v", k, hw.Flavours)
		}
	}
}
