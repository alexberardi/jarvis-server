# web/admin: the admin SPA

The React/Vite admin UI that jarvisd serves on the admin listener (port 7710,
`JARVIS_PORT_ADMIN`). Design and port plan: [`docs/admin/00-inventory.md`](../../docs/admin/00-inventory.md).

## Provenance

Copied (plain copy, AQ10) from the `jarvis-admin` repo at
**`74e3637cc7abc71df8fd8df0d033844f9c724aca`**: `src/`, `index.html`, `public/`, `tests/`,
`package.json`, `package-lock.json` and the Vite, TypeScript and ESLint configs. The Fastify
`server/`, Docker files, install scripts, `bundle/` and `prds/` were left behind. History up to
that commit lives in the `jarvis-admin` repo, which is frozen from that SHA (fixes only, for the
legacy Docker stack) and archived at the prod cutover.

Changes made on the copy:

- `vite.config.ts`: build output goes to `dist/ui`; the dev server runs on :5173 and proxies
  `/api` and `/health` to jarvisd (`JARVISD_ADMIN_URL`, default `http://localhost:7710`)
  instead of the Fastify backend on 7711.
- `eslint.config.js`: dropped the `server/dist` ignore.

## Build and embed

```
make admin                      # = npm --prefix web/admin ci && npm --prefix web/admin run build
```

`embed.go` (package `adminui`) embeds `dist/` with `//go:embed all:dist`.

- `dist/ui/` is the Vite output. It is gitignored.
- `dist/placeholder.html` is committed. Without a UI build, jarvisd serves it for every page,
  so `go build ./...` and `go test ./...` work with no Node installed.
- Release builds run the SPA build first, then
  `go test ./web/admin -run TestEmbeddedUI -tags release_ui`, which fails if only the
  placeholder would be embedded.

## Develop

- Node 22 LTS (`.nvmrc`).
- `npm run dev`: Vite on :5173 against a running `jarvisd serve`.
- `npm run lint`, `npx tsc -b`, `npm test` (Vitest + jsdom).
- `JARVIS_ADMIN_UI_DIR=web/admin/dist/ui jarvisd serve` serves a fresh build from disk without
  rebuilding jarvisd.
