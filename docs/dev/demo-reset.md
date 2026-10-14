# Reset a dev server to demo data

Start a dev jarvisd over from an empty database with the demo household from
`scripts/seed-dev.py`, then reconnect the desktop agent and nodes. Use it before taking
screenshots or recordings, or whenever dev data has drifted. It takes about five minutes.

Assumes a Linux system install (the service runs as `jarvisd`, home `/var/lib/jarvisd`),
optionally run through `scripts/dev-run.sh`. Downloaded models and engines are kept: only the
database is replaced.

## 1. Stop jarvisd and move the database aside

```sh
# If scripts/dev-run.sh is running, Ctrl-C it first. It restarts the installed service on exit,
# so stop that too:
sudo jarvisd service stop

sudo -u jarvisd sh -c 'd=/var/lib/jarvisd/old-$(date +%Y%m%d-%H%M%S) && mkdir -p "$d" && mv /var/lib/jarvisd/jarvis.db* "$d"/'
```

Moving the database (`jarvis.db`, `-wal`, `-shm`) aside instead of deleting it keeps a way back:
stop jarvisd and move it back. Delete the `old-*` directories once you no longer need them.
`seed-dev-state.json` (the demo nodes' keys) can stay. The seeder notices the nodes are gone and
provisions them again.

## 2. Start jarvisd on the empty database

```sh
scripts/dev-run.sh            # this checkout's build; or: sudo jarvisd service start
```

**Don't finish the setup wizard in the browser.** The seeder creates the first account itself
with the setup token. If you create one first, the seeder stops and asks you to reset again.

## 3. Seed

```sh
cp scripts/seed-dev.py /tmp/seed-dev.py         # the jarvisd account can't read your home directory
sudo -u jarvisd python3 /tmp/seed-dev.py --home /var/lib/jarvisd
```

It reads the setup token from `/var/lib/jarvisd/setup-token` and finishes by checking every
login and both demo nodes. The logins all use the password `DevPass123!`:

| Account | Who | Role |
|---|---|---|
| `test@jarvisautomation.io` | Jordan | admin (first user) |
| `carl@jarvisautomation.io` | Carl | admin |
| `sam@jarvisautomation.io` | Sam | member |
| `robin@jarvisautomation.io` | Robin | power user |

Then sign in to the admin UI (port 7710) as Jordan and finish the remaining wizard steps:
hardware, models and privacy. **The models step is required.** Model assignments live in the
database, so a fresh one has no live model, and every voice turn fails until you pick one (the
already downloaded models are listed, so nothing downloads again). For the "knows who's talking"
demo, also turn on speaker recognition (`voice.recognition_enabled`), which is off by default.

## 4. Reconnect the desktop agent

The agent's enrollment belongs to the old database, so enroll again with `--force`:

```sh
dist/jarvis-agent enroll --force --server http://localhost:7700 --email test@jarvisautomation.io
scripts/agent-dev-run.sh
```

If `dist/jarvis-agent` doesn't exist yet, run `scripts/agent-dev-run.sh` once to build it. It
will then fail to authenticate, and that is expected.

## 5. Reconnect physical nodes

A node provisioned against the old database is unknown to the new one. Factory-reset it and
provision it again from the mobile app, signed in as Jordan, into the demo household.

## Before screenshots or recordings

Rerun the seeder right before you capture anything:

```sh
sudo -u jarvisd python3 /tmp/seed-dev.py --home /var/lib/jarvisd
```

Each run replaces the demo content with fresh timestamps. jarvisd deletes voice transcripts
older than 7 days daily, so old seed data thins out. Logins, the household and nodes are left
as they are.

Only demo data goes in marketing material: no real names, emails, Wi-Fi names, notifications or
window titles in a shot.

For the admin-UI screenshots, use [`scripts/marketing/capture-admin.mjs`](../../scripts/marketing/README.md).
It signs in as Jordan, captures each page at desktop and phone sizes in both themes, masks the host
name, uptime and disk numbers, and can't change the server.

## Troubleshooting

| Symptom | Cause |
|---|---|
| `jarvis-agent enroll`: `401 Invalid email or password` with the right password | The seeder hasn't run on this database yet, so the account doesn't exist. Run step 3. |
| The seeder says setup is already finished with another password | Someone completed the browser wizard first. Start again from step 1. |
| `python3: can't open file …: Permission denied` | The script is in your home directory. Copy it to `/tmp` (step 3). |
| jarvisd log: `cc: start conversation failed … unknown prompt provider ""` | No live model is assigned yet (the prompt provider comes from it). Finish the wizard's models step, or pick one in admin → Models. |
| jarvisd log: `stt: speaker_recognition_disabled` | Speaker recognition is off by default. Turn on `voice.recognition_enabled` if the demo needs it. |
| The agent starts but nothing works / 401s in its log | It's still enrolled with the old database. Run the `enroll --force` in step 4. |
