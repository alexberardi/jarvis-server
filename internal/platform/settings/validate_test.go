package settings

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/platform/db"
)

func newValidatedService(t *testing.T) *Service {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "jarvis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	vdefs := []Definition{{Key: "x.even", Category: "x", Type: String, Default: "",
		Validate: func(v any) error {
			if s, _ := v.(string); s == "odd" {
				return errors.New("must not be odd")
			}
			return nil
		}}}
	s, err := New(d, "vt", vdefs, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestValidateRejectsBeforeStoring(t *testing.T) {
	s := newValidatedService(t)
	ctx := context.Background()
	sc := Scope{HouseholdID: "hh"}
	if err := s.Set(ctx, "x.even", "even", sc); err != nil {
		t.Fatal(err)
	}
	err := s.Set(ctx, "x.even", "odd", sc)
	if !errors.Is(err, ErrInvalidValue) || InvalidValueMessage(err) != "must not be odd" {
		t.Fatalf("got %v", err)
	}
	if v := s.String(ctx, "x.even", sc); v != "even" {
		t.Fatalf("rejected value was stored: %q", v)
	}
	// nil clears and is never validated; a key without a validator always passes.
	if err := s.Validate("x.even", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(ctx, "x.even", nil, sc); err != nil {
		t.Fatal(err)
	}
	if err := s.Validate("no.such", "v"); !errors.Is(err, ErrUnknownKey) {
		t.Fatal(err)
	}
}

func TestRouterPutValidationError(t *testing.T) {
	s := newValidatedService(t)
	code, m := serve(t, s, "PUT", "/settings/x.even?household_id=hh", `{"value":"odd"}`, allow)
	errObj, _ := m["detail"].(map[string]any)["error"].(map[string]any)
	if code != 422 || errObj["type"] != "validation_error" || errObj["code"] != "invalid_value" ||
		errObj["message"] != "Invalid value for x.even: must not be odd" {
		t.Fatalf("%d %v", code, m)
	}
	if code, _ := serve(t, s, "PUT", "/settings/x.even?household_id=hh", `{"value":"ok"}`, allow); code != 200 {
		t.Fatal(code)
	}
}
