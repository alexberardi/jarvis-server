package db

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"
)

// lastCounter is each migrations directory's last sequential version (00001, 00002, …). Every
// migration after it is named by a timestamp, YYYYMMDDHHMMSS_<name>.sql: two branches that each
// took "the next counter" both made cc's 00360 (2026-11-10), and jarvisd refused to start. A
// timestamp never collides, and the runner allows out of order (migrate.go), so each database
// applies whatever it hasn't seen. cc's 370 leaves room for renumbering one of those two.
var lastCounter = map[string]int64{
	"internal/modules/auth/migrations":          2,
	"internal/modules/cc/migrations":            370,
	"internal/modules/config/migrations":        3,
	"internal/modules/llm/migrations":           3,
	"internal/modules/logs/migrations":          2,
	"internal/modules/notifications/migrations": 2,
	"internal/modules/ocr/migrations":           1,
	"internal/modules/recipes/migrations":       2,
	"internal/modules/stt/migrations":           2,
	"internal/modules/tts/migrations":           1,
	"internal/platform/queue/migrations":        1,
	"internal/platform/scheduler/migrations":    1,
}

var migrationName = regexp.MustCompile(`^(\d+)_[a-z0-9_]+\.sql$`)

// TestMigrationVersions keeps every migrations directory free of duplicate versions and new
// migrations on timestamps. A directory not listed above has no counters: all timestamps.
func TestMigrationVersions(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	dirs, err := filepath.Glob(filepath.Join(root, "internal", "*", "*", "migrations"))
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no migrations directories under %s: %v", root, err)
	}
	for _, dir := range dirs {
		rel, _ := filepath.Rel(root, dir)
		rel = filepath.ToSlash(rel)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[int64]string{}
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".sql" {
				continue
			}
			m := migrationName.FindStringSubmatch(e.Name())
			if m == nil {
				t.Errorf("%s/%s: name it YYYYMMDDHHMMSS_<snake_case>.sql", rel, e.Name())
				continue
			}
			v, _ := strconv.ParseInt(m[1], 10, 64)
			if other, dup := seen[v]; dup {
				t.Errorf("%s: %s and %s have the same version %d; jarvisd won't start", rel, other, e.Name(), v)
			}
			seen[v] = e.Name()
			if v <= lastCounter[rel] {
				continue
			}
			if ts, err := time.Parse("20060102150405", m[1]); err != nil || ts.Year() < 2026 {
				t.Errorf("%s/%s: new migrations are named by a UTC timestamp, YYYYMMDDHHMMSS_<name>.sql "+
					"(e.g. %s_<name>.sql), not the next counter: parallel branches pick the same counter",
					rel, e.Name(), time.Now().UTC().Format("20060102150405"))
			}
		}
	}
}
