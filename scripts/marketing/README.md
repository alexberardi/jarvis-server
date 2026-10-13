# Marketing captures

`capture-admin.mjs` takes the admin-UI screenshots for the landing page and docs. It is
reproducible: a fixed viewport, waits on selectors (never sleeps), frozen animations, and
the same masks every run.

It signs in as a demo account (Jordan, `test@jarvisautomation.io`, by default) and captures each
shot at 1440×900 (desktop) and 390×844 (phone), in the light and dark themes, at device scale
factor 2:

| Shot | Page |
|---|---|
| `dashboard` | Dashboard |
| `models` | Models: jobs and the live label on this machine |
| `models-gpu` | Models, scrolled to the running engines on the GPU |
| `nodes` | Nodes |
| `traces` | Traces, with a multi-turn conversation expanded |
| `settings` | Settings |
| `wizard-models` | The setup wizard's Language model step (opened at a saved step, which writes nothing) |

Files are named `<shot>-<desktop|phone>-<light|dark>.png`.

## Before you run it

Reset the server to demo data and rerun the seeder right before capturing
([docs/dev/demo-reset.md](../../docs/dev/demo-reset.md)). Fresh timestamps make the traces and
"last seen" read well. The demo nodes show as online for about 15 minutes after a seed.

## Run

Playwright isn't a repo dependency. Install it anywhere outside the repo:

```sh
DIR=/tmp/jarvis-capture
npm i --prefix "$DIR" playwright
PLAYWRIGHT_BROWSERS_PATH="$DIR/browsers" npx --prefix "$DIR" playwright install chromium-headless-shell

PLAYWRIGHT_PREFIX="$DIR" PLAYWRIGHT_BROWSERS_PATH="$DIR/browsers" \
  node scripts/marketing/capture-admin.mjs --out /tmp/shots --hide-room desktop
```

It runs headless only. Useful flags:

- `--base URL`: the admin UI (default `http://localhost:7710`). To capture an admin build newer
  than the running jarvisd, serve it with the Vite dev server, which proxies `/api` to jarvisd:
  `cd web/admin && npm ci && npx vite --port 5199 --strictPort`, then `--base http://localhost:5199`.
- `--only dashboard,traces`, `--themes dark`, `--viewports phone`: a subset.
- `--hide-room ROOM`: drops the traces of every node in that room, for example a real desktop
  agent, whose requests can name real apps and windows.
- `--redact STRING`: covers any other text you don't want in a shot (repeatable).
- `--host-label NAME`: what the host name is replaced with (default `jarvis-home`).

## What it guarantees

- **Read-only.** In the browser, every request other than a GET (or the token refresh) is
  aborted and logged as `blocked`. A stray click can't change settings, models or nodes.
- **No real host details.** These are masked by script injected into the page, never by changing
  the app:
  - the host name is replaced everywhere, including a desktop node named after the machine;
  - IPv4 addresses, the uptime and the free disk space are covered by an opaque pill;
  - tooltips are dropped, since they carry the data directory, listener ports and OS release.

Check each image before it goes public anyway. A new page or field can show something the masks
don't know about. Keep the images out of this repo.
