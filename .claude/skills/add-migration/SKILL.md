---
name: add-migration
description: Add or change a database table or column in jarvisd, which means a goose SQLite migration under internal/modules/<module>/migrations/ (or internal/platform/{queue,scheduler}/migrations/). Use it whenever a change needs a new table, column, index or data fix in jarvisd's database.
---

# Adding a migration

Each module has its own goose migrations, embedded in the binary
(`//go:embed migrations/*.sql`, e.g. `internal/modules/cc/embed.go`) and tracked in their own
version table, `goose_<module>`. `internal/platform/module/runner.go` applies every module's
pending migrations at `serve` start, so there is no separate migrate step.
`jarvisd migrate status` shows the state. Out-of-order versions are allowed (`docs/decisions/2026-10-07-out-of-order-migrations-are-applied.md`, `WithAllowOutofOrder` in `internal/platform/db/migrate.go`).

## 1. Write the file

`internal/modules/<module>/migrations/<NNNNN>_<what>.sql`. Pick a number above the module's
highest. cc steps by tens (`00150_node_contacted.sql`, `00160_test_install.sql`,
`00170_node_kind.sql`); the others count up by one (`auth/00002_display_name.sql`).

```sql
-- Why this exists (plan item / decision / doc section). What NULL means for old rows.
-- +goose Up
ALTER TABLE cc_nodes ADD COLUMN node_kind TEXT;

-- +goose Down
ALTER TABLE cc_nodes DROP COLUMN node_kind;
```

- Table names carry the module prefix (`cc_`, `auth_`, …). Every module shares one SQLite file.
- **Forward-only and additive within a release series.** Add nullable columns or columns with
  defaults, and new tables. A drop or rename breaks the `jarvisd.prev` rollback without a
  snapshot restore, so it must go in the release notes
  (docs/install/00-installers.md §4.3). Ask the user before writing one.
- New columns are usually validated by the API, not the schema (auth 00002 `display_name`).
  The baseline's CHECK constraints are guarded by `TestBaselineChecks`.
- **Write a real Down.** `TestBaselineDownRemovesEverything` (cc) runs every migration down to 0
  and back up.

## 2. Tests

- Test the behaviour that needs the column in the module's own `_test.go`. Helpers like `migrated(t)`
  in `internal/modules/cc/migrations_test.go` give you a migrated temp DB.
- cc has guard lists in `migrations_test.go`: add a new table to `expectedTables`, and never
  re-add one in `cutTables` (decisions in docs/cc/QUESTIONS.md). Use
  `TestBaselineDecisionColumns` for columns a decision requires. Non-cc modules have
  `internal/modules/baseline_noncc_test.go`.
- `CGO_ENABLED=1 go test -race ./internal/modules/<module>/ ./internal/platform/db/ ./cmd/jarvisd/`

## 3. Things that also read the schema

- **Legacy import** (`internal/legacyimport/import.go`, `jarvisd import-legacy`) inserts with
  explicit column lists. A new nullable column needs nothing. A NOT NULL column without a default,
  or a column with a legacy equivalent, needs the import updated and `import_test.go` with it.
- **Recipes import** (`jarvisd import-recipes`, `cmd/jarvisd/import_recipes*.go`) for recipes tables.
- **Docs:** describe the table or column in `docs/schema/<module>.md` and the subsystem doc
  (`docs/cc/NN-*.md`), and name the migration number there (e.g. "cc migration 00160").
- **Contract:** if the column surfaces in a response, use the `wire-change` skill.

## 4. Release note

`cut-rc` lists the migrations since the last tag with
`git diff --stat <prev-tag> origin/main -- ':(glob)internal/**/migrations/**'`. Mention the
migration in the commit message and the PR ("auth migration 00002").

## Gotchas

- An older binary refuses to start on a DB a newer one migrated (`serve --allow-downgrade`
  overrides). So test an upgrade path, never a downgrade, on a real home.
- Never edit a migration that a published tag already contains (check with
  `git tag --contains <commit>`). Installed homes have already applied it. Add a new one instead.
- A sandbox (`scripts/sandbox up`) migrates its own `.sandbox/<name>/home`. Check it with
  `.sandbox/<name>/jarvisd migrate status --home .sandbox/<name>/home`, and never point it at
  `/var/lib/jarvisd`.
