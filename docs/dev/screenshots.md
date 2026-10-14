# Screenshots of the admin UI

`scripts/screenshots` signs in to a `scripts/sandbox` instance through the real login form,
as the superuser or any other seeded user (`--user`). It visits every page that user has and saves full-page PNGs at phone, tablet and desktop widths, in
light and dark. Each page also gets a report of console errors, failed requests and horizontal
overflow. It lets an agent (or a reviewer) see the UI without a browser. The Playwright code is
in `tools/screenshots/`, and Playwright stays out of `web/admin`'s dependencies.

## Run it

```sh
scripts/sandbox up --name shots --ui          # build the SPA + jarvisd, start a seeded sandbox
scripts/screenshots --sandbox shots           # the superuser: ~18 pages x 3 viewports x 2 themes, ~2 min
scripts/screenshots --sandbox shots --pages dashboard,models --viewports phone --themes light --tiles 1600
scripts/screenshots --sandbox shots --user member                  # Sam: chat, addons, inbox, account
scripts/screenshots --sandbox shots --user member --pages chat,dashboard   # dashboard: the admin-only notice
scripts/sandbox down --name shots
```

On its first run the script installs Playwright into `tools/screenshots/node_modules` and
downloads Chromium if the cache doesn't have it.

| Option | Default | |
|---|---|---|
| `--sandbox NAME` | `default` | Reads `.sandbox/NAME/sandbox.env`. The sandbox must be up and serving the SPA (`--ui`). |
| `--user WHO` | `superuser` | Who signs in, all with the seeded password: `superuser` (Jordan), `admin` (Carl, a household admin), `power` (Robin, a power user), `member` (Sam), or a seeded user's email. The roles' emails come from `sandbox.env` (`SBX_EMAIL_ADMIN`, `_POWER`, `_MEMBER`, read from the household by `scripts/sandbox up`; `scripts/seed-dev.py` seeds them). Only the superuser has the admin pages. |
| `--pages a,b` | all the user has | Sidebar pages as slugs (`chat`, `addons`, `inbox`, `dashboard`, `models`, `settings`, `connections`, `logs`, `traces`, `nodes`, `users`, `update`), plus `account`, `trace-detail` (only when the sandbox has a trace), `login`, `change-password`, `setup`, `not-found` and `nav-drawer` (the phone/tablet menu, opened). A value starting with `/` is a raw path. Without `--pages`, the superuser gets all of them and anyone else gets `chat`, `addons`, `inbox` and `account`. |
| `--viewports` | `phone,tablet,desktop` | 390x844 and 820x1180 with mobile emulation and touch, and 1440x900. |
| `--themes` | `light,dark` | Sets `jarvis-admin-theme` in localStorage and emulates `prefers-color-scheme` to match. |
| `--out DIR` | `.sandbox/NAME/shots/<timestamp>/` | `<timestamp>-<user>/` for a `--user` other than the superuser. |
| `--tiles PX` | off | Also cuts each page taller than PX into PX-high pieces (`<file>.t01.png`, ...). Phone pages can reach 6000px, and an image viewer shrinks that until it can't be read. Use `--tiles 1600` when an agent will Read the shots. |
| `--settle-ms N` | `600` | Extra wait after the page goes quiet and its spinners are gone. |

## Output

Each shot is saved as `<page>__<viewport>__<theme>.png`, and `index.json` holds one record per shot:

- `finalPath`, `title` and `heading`: where the page ended up after any redirects.
- `adminOnly`: `true` when a user who isn't the superuser asked for an admin page and got the
  "This page is for the install's administrator" notice. That is the expected page for them, so
  it is not an error; the summary line says `administrator only (notice)`.
- `consoleErrors`, `pageErrors`, `failedRequests` and `httpErrors` (status 400 and up). Aborted requests are left out, because streams are cut off when the page closes.
- `overflow`:
  - `overflowPx` is how far the page scrolls sideways. It covers both the document and the shell's `<main>`, which is the element that actually scrolls.
  - `offenders` lists the outermost elements that stick out past the viewport. An inner `overflow-x` container doesn't clip them.
  - `innerScrollers` lists containers that scroll sideways inside the page.
  - `truncated` lists text cut off with an ellipsis.

`index.json` also records who signed in (`user`: email and whether a superuser). The script
also prints a summary: one line per shot, then the overflow offenders, then the
inner scrollers and truncated text at the narrowest viewport. It exits 1 when a page failed to
load.

## How it works

- **Login:** it fills the login form once, as the `--user`, and keeps the session (localStorage) for every later context. It reads the signed-in user the SPA stored to tell a superuser from anyone else. A superuser's sign-in lands on `/setup`, which resumes the wizard when setup hasn't finished, as in a fresh sandbox. The admin pages don't gate on setup, so the script goes straight to them.
- **Members:** anyone but the superuser has Chat, Add-ons, Inbox and Account. The admin pages are known by name for every user, so asking for one as a member shoots the notice `RequireSuperuser` shows them, and marks the shot `adminOnly`.
- **Full height:** `AppShell` is `h-dvh` and scrolls `<main>`, not the document. Overflow is measured first, in the natural layout. Then `<main>` and its ancestors are let grow to their content, so the full-page shot shows the whole page.
- **Adding pages:** sidebar pages are found from `#admin-sidebar nav a[href]` (`/admin/models` is named `models`). Add other pages to `EXTRA_PAGES`, a new admin page to `ADMIN_PAGES`, and a member page to `MEMBER_PAGES` in `tools/screenshots/screenshots.mjs`.
