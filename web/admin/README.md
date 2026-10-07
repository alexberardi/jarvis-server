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
- A5–A9 rewrote it for jarvisd (no Docker, no compose, no Fastify backend). Everything goes
  through the admin listener's same-origin gateway (`/api/*`, superuser-gated; see the inventory
  §3.2 and the "As built" notes).

## Pages

| Route | What | Backend |
|---|---|---|
| `/setup` | First-run wizard: Check → Account → Hardware → Models → Privacy → Done (AD3, AD3a) | `/api/doctor`, `/api/auth/setup` (setup token), `/api/setup/state`, `/api/llm/v1/*`, `PUT /api/settings/{service}/{key}` |
| `/dashboard` | System, health check, model states, nodes online, recent requests, update banner | `/api/system/info`, `/api/doctor`, `/api/setup/state`, `/api/llm/v1/models/labels`, `/api/cc/api/v0/admin/nodes`, `/api/traces`, `/api/update` |
| `/models` | Model manager | `/api/llm/v1/*`, `/api/prompt-provider` |
| `/settings` | Every module's settings; restart jarvisd (AD8, hidden when the route 404s) | `/api/settings`, `POST /api/system/restart` |
| `/connections` | Listeners, external services, app clients (key shown once) | `/api/connections*` |
| `/logs` | Filtered logs and a live tail (fetch stream, resumes with `?after=`) | `/api/logs*` |
| `/traces`, `/nodes`, `/users` | Request traces, households and nodes, users | `/api/traces*`, `/api/admin/*` |
| `/update` | Update opt-in and verdict (never "up to date" without a real check); self-update when the route exists | `/api/update*` |

Server routes that may not exist yet (`POST /api/system/restart`, `POST /api/update/apply`) are
feature-detected: the button shows until the route answers 404 once (`src/lib/features.ts`).

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
- `npm run lint`, `npx tsc -b`, `npm test` (Vitest + jsdom). With Node 25+ the tests use an
  in-memory Storage (`tests/setup.ts`). TanStack Query v5 passes a context argument to
  `mutationFn`, so wrap API functions in arrows instead of passing them by reference.
- `JARVIS_ADMIN_UI_DIR=web/admin/dist/ui jarvisd serve` serves a fresh build from disk without
  rebuilding jarvisd.
