# Prod cutover runbook: legacy Docker stack → jarvisd (I9)

**Status: draft, 2026-10-07. Nothing in it has been run on prod.** Prod stays read-only until the
user says go. The facts in §1 come from a read-only survey of prod on 2026-10-07 (23:30Z):
`docker ps`/`inspect` without environment values, `nvidia-smi`, `df`, `ss`, `systemctl`, and
`SELECT count(*)` queries. Re-check them at T-1 (§3), because prod changes.

What this runbook does (decisions it rests on):

- **ID6 clean start.** No `import-legacy`. Accounts, households, nodes, memories, routines, phonebook
  and inbox are not carried over. The legacy Postgres and volumes are kept, stopped and untouched, for
  manual recovery and rollback.
- **LD5.** Models and LLM settings start clean through the wizard (the 150 GB `~/.jarvis/compose/.models`
  is left alone; §4.5 has a no-download fallback for the 27B file).
- **ID7.** `install.sh --stop-legacy` stops the legacy containers (`docker update --restart=no` +
  `docker stop`, never `down`): every running one in the legacy Compose project or named `jarvis-*`,
  and turns off the legacy admin's user unit. Rollback is `jarvisd` off + the legacy containers
  started again with their restart policies put back, and the admin unit on again (§6).
- **ID8 / AD3a.** The wizard's Privacy step; prod used the push relay, so it goes on.
- **A10 node technique.** Active nodes are re-pointed without a factory reset (keeps Wi-Fi): register the
  node's **existing** `node_id` on jarvisd with a provisioning token, swap `api_key` in its `config.json`,
  restart the node service ([A10-rehearsal.md](A10-rehearsal.md) "Node (jarvis-dev) without physical
  access").
- **ID12.** Prod's model, Qwen3.8-27B, has a strict chat template; the catalog flags it
  `fold_system_messages`, so it works on jarvisd (this is also the likely cause of prod's 2026-10-04 500s).

Placeholders: `<tag>` the release to install, `<kitchen-node>` the kitchen Pi's mDNS name (root
`CLAUDE.md`), `<you>` the superuser's email. Every command runs on prod as the `jarvis` login user
(`ssh jarvis@10.0.0.107`) unless it says "on the node" or "from a LAN laptop".

---

## 1. Prod as surveyed (2026-10-07)

**Host.** Ubuntu 24.04.4, kernel 6.8, systemd 255, Ryzen 9 5950X (32 threads), 125 GB RAM, 2× RTX 3090
24 GB (driver 580.173.02). `jarvis` is in `sudo` and `docker`. `/` has **124 GB free** of 886 GB (86% used);
`/mnt/fast` (3.4 TB free) is a good backup target. No `minisign`, no `sqlite3`; `jq` and `curl` present.
ufw is installed but **disabled** (`ENABLED=no`), firewalld absent, avahi-daemon inactive.

**GPUs today.** GPU 0: `llama-server-bg` 20.2 GB + llm-proxy 0.4 GB. GPU 1: `llama-server` (live) 18.1 GB +
whisper 2.5 GB + TTS 1.5 GB. Both nearly full, so **every legacy GPU container must be stopped before
jarvisd can load a model**. `--stop-legacy` does that (it used to stop only `jarvis-*` names and missed
the llama-servers; fixed 2026-10-07, see below), and `jarvisd doctor` warns while the legacy stack holds GPU memory.

| Legacy LLM | live (`llama-server`, GPU 1) | background (`llama-server-bg`, GPU 0) |
|---|---|---|
| Model | `Qwen3.8-27B-UD-Q4_K_M.gguf` (same size as catalog `qwen3.8-27b`) | same |
| mmproj | `mmproj-F16.gguf` (same size as catalog `qwen3.8-27b-mmproj`) | none |
| Context / slots | 13312, llama-server's default 4 slots | 131072, 1 slot |
| KV / flash | f16 / default | q8_0 / on |
| Thinking | off (`enable_thinking=false`, budget 0) | unrestricted (budget -1) |

Other voice settings: Whisper large-v3-turbo on GPU, Kokoro `bm_george` at 1.25 (jarvisd's defaults
already), speaker recognition on at 0.49 (ECAPA; jarvisd uses ERes2Net with 0.43, and voiceprints do not
carry over).

**Containers.** Compose project `jarvis` in `~/.jarvis/compose` (every container `restart=unless-stopped`
except `jarvis-minio-init`, `on-failure`), plus project `jarvis-node` in `~/jarvis-node` and an unrelated
`plex`.

`--stop-legacy` (and the doctor's "legacy stack" check) picks a running container when it is named
`jarvis-*`, or its Compose labels put it in project `jarvis` (what the jarvis-admin installer pins in
`~/.jarvis/compose`), in a `jarvis-*` project (source checkouts, the dockerized node), or in any project
whose working directory is `~/.jarvis/compose`. Nothing else is touched (`internal/doctor/legacy.go`
`LegacyContainer`; the scripts apply the same rule).

| Group | Containers | Stopped by `--stop-legacy`? |
|---|---|---|
| Core (hold jarvisd's ports) | `jarvis-config-service` 7700, `-auth` 7701, `-logs` 7702, `-command-center` 7703, `-llm-proxy-api` 7704, `-whisper-api` 7706, `-tts` 7707, `-admin` 7710, `-notifications` 7712, `-ocr-service` 7031, `-mosquitto` 1884/9883 | yes |
| Other `jarvis-*` | `-settings-server` 7708, `-phone-gateway` 7713, `-web` 7722, `-recipes-server` 7030, `-recipes-worker`, `-ocr-worker`, `-postgres`, `-redis`, `-minio`, `-seaweedfs`, `-loki`, `-grafana` 3001, **`-demo-node` 7771** (a dockerized node, project `jarvis-node`) | yes |
| In project `jarvis`, not named `jarvis-*` | **`llama-server`, `llama-server-bg`, `llm-proxy-worker`** (GPU), `go2rtc` 1984 | **yes** (by project; Q7 for go2rtc) |
| Unrelated | `plex` | no |
| Already exited | `ollama` (exited 8 days, 16 GB volume), `jarvis-minio-init` | — (only running ones are touched) |

T-1 (§3): confirm the project labels with
`docker ps --format '{{.Names}} {{.Label "com.docker.compose.project"}}'`; every legacy container should
say `jarvis` (or `jarvis-node`), `plex` nothing or another name.

**Host services.** Two legacy pieces run outside Docker as `jarvis` **user** units:

- `jarvis-admin.service`: the legacy admin backend, listening on **7711** (the 7711 in 00-installers §5.2).
  jarvisd doesn't use 7711, so it is not a port clash, but its "reconcile" button runs `docker compose up -d`,
  which would bring the GPU containers back. `--stop-legacy` runs `systemctl --user disable --now
  jarvis-admin.service` for the invoking user (under `sudo`, for `$SUDO_USER` through
  `systemctl --user -M $SUDO_USER@`); prod's unit has linger on, so its user manager is reachable.
- `cloudflared-jarvis.service`: a Cloudflare tunnel (`~/.cloudflared/jarvis-services.yml`) that maps public
  hostnames to `localhost` 7700–7708, 7710, 7712, 7713, 7722, 7030, 7031, 9883 and SSH. The system
  `cloudflared.service` serves Plex only. **The legacy registry points most services at those public
  HTTPS hostnames** (config-service, auth, CC, logs, notifications, OCR, recipes, settings, MQTT as
  `wss` on 443), so today nodes and phones reach prod through the tunnel. See §4.11 and open question Q1.

**Data (counts only).** 10 users (2 superusers), 8 households, 10 memberships: 2 households with 2 members,
6 with one. 23 CC node rows and 36 auth registrations, mostly test/canary rows from 2026-08-11/12.
49 memories, 2 routines, 0 schedules, 2 phone contacts, 17 phone calls (last 2026-08-30), 7 devices,
8 rooms, 48 recipes, 100 inbox items, 5 push device tokens (5 users), 142 pushes in the last 30 days,
13 voice transcripts and 249 traces in the last 7 days. Postgres data volume 186 MB.

**Active nodes.**

| Node | Kind | Version | Last seen | Household | Re-point |
|---|---|---|---|---|---|
| kitchen (`pi@<kitchen-node>.local`) | Pi, tarball | 0.3.1 | live | A (2 members) | §4.8, over SSH |
| "default" room (`jarvis-demo-node`) | container on prod | 0.2.0 | live | B (1 member) | §4.8, its config on prod |
| living_room | Pi, tarball | 0.3.1 | 2026-09-02 | C (2 members) | when it comes back (Q4) |
| 2 bedroom, 1 living_room, 1 kitchen | Pi, tarball | 0.1.x | Jun–Aug | — | not migrated |

Settings in use on prod that the wizard or the admin must set again: web search on (household), ambient
context on, attention on, phone calls on, `household.location` (household), speaker recognition on.

---

## 2. Go / no-go

All must be **yes** before the window starts. Record the answers in STATUS.

| # | Check | How |
|---|---|---|
| G1 | A **signed** release `<tag>` exists (needs repo secrets `MINISIGN_SECRET_KEY` + `MINISIGN_PASSWORD`, STATUS "Next" (1)) and its `verify` job is green | GitHub release page lists `SHA256SUMS.minisig` |
| G2 | The same `<tag>` passed a fresh install + voice turn on the dev box (A10 again, system unit if anyone can sudo there) | STATUS entry |
| G3 | Kitchen node software has the "before cutover" node changes: `chat_text()` → `/api/v0/node/llm/chat` (EXTERNAL-CHANGES D5) and per-node MQTT credentials (D4/D7). jarvis-dev passed A10 with them; confirm 0.3.1 includes them (Q5) | node-setup release notes / `git tag --contains` |
| G4 | Mobile app build in users' hands hides the Forge test-install screen (EXTERNAL-CHANGES, "before cutover") or that screen is accepted as broken | app release |
| G5 | Users told: accounts, memories, routines, contacts and voice enrollment start over; the app needs a fresh sign-up; browser chat (jarvis-web) and recipes go away (Q2, Q3) | message sent |
| G6 | Off-LAN access decision made (Q1) | answer recorded |
| G7 | Pre-flight §3 done within 24 h: backups written and readable, ≥ 40 GB free on `/`, `sudo` works | §3 checklist |
| G8 | A 2-hour window when nobody needs voice, chat, push or phone | — |
| G9 | Someone can reach the kitchen node over SSH from the LAN (no physical access needed) | `ssh pi@<kitchen-node>.local true` |

No-go triggers during the window: §4.3 install not healthy after one retry; no model ready 60 min after
the Models step started; the kitchen node not on MQTT 5 min after its restart; no voice turn by T+2 h.
Any of these → §6 rollback.

---

## 3. Pre-flight (T-1 day, no outage)

Everything here is read-only for the legacy stack or writes only into a new backup directory.

```sh
BK=/mnt/fast/jarvis-legacy-$(date +%Y%m%d); mkdir -p "$BK"; chmod 700 "$BK"

# 3.1 Record every container's restart policy and state (rollback restores exactly this).
docker inspect --format '{{.Name}} {{.HostConfig.RestartPolicy.Name}} {{.State.Status}}' $(docker ps -aq) \
  | sed 's#^/##' > "$BK/containers.txt"
cat "$BK/containers.txt"     # expect ~31 lines: "jarvis-auth unless-stopped running", ..., "ollama unless-stopped exited"

# 3.2 What is listening, GPU use, disk (for comparison afterwards).
ss -ltnH | awk '{print $4}' | sort -u > "$BK/listen-before.txt"
nvidia-smi --query-gpu=index,memory.used,memory.total --format=csv > "$BK/gpu-before.txt"
df -h / /mnt/fast                                   # expect / ≥ 40 GB available (124 GB on 2026-10-07)

# 3.3 Logical dump of every legacy DB (dress rehearsal; repeated at T-0, §4.1).
docker exec jarvis-postgres pg_dumpall -U jarvis | gzip > "$BK/pg_dumpall-t1.sql.gz"
ls -lh "$BK/pg_dumpall-t1.sql.gz"                   # expect tens of MB; ~10 s
gunzip -c "$BK/pg_dumpall-t1.sql.gz" | grep -c '^CREATE DATABASE'   # expect 9

# 3.4 Config that rollback or a later look-up needs (contains secrets: the dir is 0700).
tar czf "$BK/compose-dir.tgz" --exclude=.models -C ~/.jarvis compose   # .env files, compose files, backups
cp -p ~/.cloudflared/jarvis-services.yml "$BK/"
sudo cp -p ~/jarvis-node/config/config.json "$BK/demo-node-config.json"
# The public hostname of every service, which §4.6.1 enters into jarvisd (legacy stops at T-0).
curl -s localhost:7700/services | jq -r '.services[] | "\(.name) \(.url)"' | sort > "$BK/legacy-services.txt"
grep -E 'hostname|service' ~/.cloudflared/jarvis-services.yml > "$BK/tunnel-ingress.txt"

# 3.5 Install minisign so install.sh *requires* the release signature (00-installers §8.3).
sudo apt-get install -y minisign
```

Re-run the §1 survey commands that matter and stop if anything moved: the two llama-server containers
still the only big GPU users, the active node list unchanged, nothing new on 7700–7712, 7031, 1884, 9883.

Also before the window:

- Note the kitchen node's `node_id` and config path **on the node** (read only):
  `ssh pi@<kitchen-node>.local 'ls -l /opt/jarvis-node/config.json; jq -r .node_id /opt/jarvis-node/config.json; jq -r "keys[]" /opt/jarvis-node/config.json; systemctl list-units --type=service --no-legend | grep -i jarvis'`.
  Write down the node_id, whether a `household_id` key exists, the file's owner, and the service unit name.
- Same for the demo node: `sudo jq -r '.node_id, (keys|join(" "))' ~/jarvis-node/config/config.json`.
- Twilio (if phone stays, Q6): have the account SID, auth token and from number at hand (they are in the
  legacy phone gateway's env, inside `compose-dir.tgz`); decide which public hostname will carry
  `/phone/media/` (§4.10).

---

## 4. Cutover (T-0)

Budget about 75 min with a fast download, about 2 h at 10 MB/s. Times are from A10 where measured.

### 4.1 Freeze and final dump (5 min)

1. Announce the outage.
2. Final dump while Postgres still runs:
   ```sh
   BK=/mnt/fast/jarvis-legacy-$(date +%Y%m%d)
   docker exec jarvis-postgres pg_dumpall -U jarvis | gzip > "$BK/pg_dumpall-t0.sql.gz" && ls -lh "$BK"
   ```

### 4.2 See what `--stop-legacy` will stop (1 min)

No manual stop is needed any more: `--stop-legacy` stops the llama-servers, `llm-proxy-worker` and
`go2rtc` (project `jarvis`) as well as every `jarvis-*`, and turns off the legacy admin unit, so nothing
brings the stack back during the window. Look at the list first (read-only):

```sh
docker ps --format '{{.Names}};{{.Label "com.docker.compose.project"}};{{.Label "com.docker.compose.project.working_dir"}}' |
  awk -F';' '{wd=$3; sub(/\/$/,"",wd)} $1 ~ /^jarvis-/ || $2 == "jarvis" || $2 ~ /^jarvis-/ || wd ~ /\/\.jarvis\/compose$/ {print $1}'
# expect the ~28 running legacy containers of §1 including llama-server, llama-server-bg, llm-proxy-worker,
# go2rtc and jarvis-demo-node; never plex
systemctl --user is-enabled jarvis-admin.service    # enabled (it will be disabled and stopped)
```

If something on that list must keep running (go2rtc, Q7), start it again after §4.3 with
`docker update --restart=unless-stopped go2rtc && docker start go2rtc`.

### 4.3 Install (2–3 min)

```sh
cd /tmp
curl -fsSLO https://github.com/alexberardi/jarvis-server/releases/download/<tag>/install.sh
less install.sh                                     # read it; it is ~300 lines
sh install.sh --version <tag> --stop-legacy         # prompts for sudo
```

`--version` is needed while `<tag>` is a prerelease (`releases/latest` only sees full releases).

Expected, in order (no firewall prompt: ufw is disabled, so doctor has nothing to fix):

1. `SHA256SUMS signature verified.` (minisign installed in §3.5; a missing or bad signature is fatal).
2. Download of ~20 MB, checksum OK, the binary prints `<tag>`.
3. `Stopping the legacy stack: …` (the §4.2 list: every `jarvis-*`, including `jarvis-demo-node`, plus
   `llama-server`, `llama-server-bg`, `llm-proxy-worker` and `go2rtc`), each now `restart=no`. About
   10–20 s. Then `Stopping the legacy admin (systemd user unit jarvis-admin.service of jarvis) and turning
   it off at login`; 7711 goes away. A warning there instead names the command to run as `jarvis`.
4. `Installing /usr/local/bin/jarvisd...`, then `jarvisd service install`: system user `jarvisd`
   (groups `video`, `render`), `/var/lib/jarvisd` 0700, `/etc/jarvisd/jarvisd.env` 0640 root:jarvisd,
   `/etc/systemd/system/jarvisd.service`, enabled and started; waits for `/health` (A10: 4 s in total).
5. `jarvisd doctor` with every check OK, `jarvisd <tag> is running.`, and the setup link
   `http://10.0.0.107:7710/setup#token=…`.

Check:

```sh
sudo jarvisd service status                          # running, health OK, exit 0
for p in 7700 7701 7702 7703 7704 7706 7707 7710 7712 7031; do
  printf '%s %s\n' $p "$(curl -s -o /dev/null -w '%{http_code}' -D - http://localhost:$p/health | grep -i '^server:' | tr -d '\r')"
done                                                 # every line "Server: jarvisd"
ss -ltnH '( sport = :1884 or sport = :9883 )'        # two listeners, jarvisd's broker
docker ps --format '{{.Names}}'                      # only plex (and go2rtc if restarted, Q7)
nvidia-smi --query-gpu=index,memory.used --format=csv # both cards near 0 until a model loads
ss -ltnH '( sport = :7711 )'                         # empty: the legacy admin is off
journalctl -u jarvisd -n 50 --no-pager               # no ERROR lines
```

If `install.sh` refuses with "another program holds jarvisd's ports" and names no legacy container,
find it with `sudo ss -ltnp` and stop it. If the service is not healthy:
`journalctl -u jarvisd -n 200`, fix, re-run `install.sh` once; still bad → §6.

If you lost the link: `sudo jarvisd setup-link`.

### 4.4 Wizard: Check → Account → Hardware (5 min)

From a LAN laptop, open the setup link. (The tunnel also publishes 7710; the token protects `/auth/setup`.)

- **Check:** all OK. The legacy check says the stack's files exist but none of its containers run.
- **Account:** the superuser. Use a real address (a `.local` domain is refused, A10 F6). The wizard
  creates the household "My Home"; rename it later to the kitchen household's name.
- **Hardware:** expect CUDA, two RTX 3090, each ~24 GB free. Set, matching prod:
  - live → device **1**, background → device **0** (two engines, one per card, as today);
  - STT on GPU, device **1** (whisper ~1.6 GB next to the live model, as today);
  - flavour: the detected CUDA build.

### 4.5 Wizard: Models (6–40 min, mostly download)

Do **not** press "Install recommended" without reading it; install these from the catalog instead:

| Catalog id | Size | Assign to |
|---|---|---|
| `qwen3.8-27b` (+ `qwen3.8-27b-mmproj` follows) | 16.46 + 0.93 GB | live **and** background |
| `whisper-large-v3-turbo` | 1.62 GB | stt |
| `kokoro-multi-lang-v1_0` | 0.35 GB | tts (default) |
| `eres2net-voxceleb-16k` | 0.03 GB | speaker (default) |
| `all-minilm-l6-v2` | 0.05 GB | embeddings (CPU) |

Plus the llama-server and whisper-server CUDA builds (~1.5 GB together). About 21 GB in all: ~6 min at
A10's 64 MB/s, ~35 min at 10 MB/s. The two install lanes (F7) let voice models finish while the 27B
downloads.

Then on the **Models page → labels editor (Advanced)**:

| Label | context | parallel | kv_cache_type | flash_attn | devices |
|---|---|---|---|---|---|
| live | 13312 (or the catalog's 16384) | 4 | f16 | auto | 1 |
| background | 131072 | 1 | q8_0 | on | 0 |

Thinking needs nothing: `llm.live.reasoning_budget` defaults to `0` (off) and
`llm.background.reasoning_budget` to `-1` (unrestricted), which is prod's setup (LD8: the user may change it).

Different settings give two engine instances (path-keyed sharing only merges identical keys). "Later
system messages" stays **auto** (= fold, from the catalog). Expected fit: live ~18.5 GB + whisper ~2 GB on
GPU 1, background ~21 GB on GPU 0; the verdicts should say it fits. The prompt provider shows
`Qwen3_14B_Compressed` "from model".

Done when every label is **ready** (the 27B loads in roughly 30–60 s per card) and
`nvidia-smi` shows two `llama-server` processes, one per GPU, plus `whisper-server` on GPU 1.

**Fallback if the download is too slow** (breaks LD5's "re-download" only for this one file): adopt
prod's own copy, whose size matches the catalog; the sha256 is checked by jarvisd.

```sh
sudo install -d -o jarvisd -g jarvisd -m 0700 /var/lib/jarvisd/import
sudo install -o jarvisd -g jarvisd -m 0600 ~/.jarvis/compose/.models/Qwen3.8-27B-UD-Q4_K_M.gguf ~/.jarvis/compose/.models/mmproj-F16.gguf /var/lib/jarvisd/import/
# then, with a superuser JWT (§4.7 shows how to get $J):
curl -s -X POST localhost:7704/v1/models/installed -H "authorization: Bearer $J" -H 'content-type: application/json' \
  -d '{"path":"/var/lib/jarvisd/import/mmproj-F16.gguf","catalog_id":"qwen3.8-27b-mmproj"}'
curl -s -X POST localhost:7704/v1/models/installed -H "authorization: Bearer $J" -H 'content-type: application/json' \
  -d '{"path":"/var/lib/jarvisd/import/Qwen3.8-27B-UD-Q4_K_M.gguf","catalog_id":"qwen3.8-27b"}'
```

(Copying is needed: the system unit runs with `ProtectHome=yes`, so `/home` is invisible to jarvisd.
Hashing 16 GB takes about a minute.)

### 4.6 Wizard: Privacy → Done (2 min)

| Choice | Set to | Why |
|---|---|---|
| Phone push notifications (relay) | **on** | prod used the project relay (5 devices, 142 pushes/30 days) |
| Web search | **on** | prod household setting was on |
| Reader proxy (`web_scraping.allow_external`) | Q8 | not determined read-only |
| Update checks | Q8 | prod's value not read |
| Memories, learning from voice | on (default) | as prod |
| Ambient context | **on** | prod had it on |
| Speaker recognition | **off for now** | nobody is enrolled yet; with it on, every speaker is "unknown" and per-user tools refuse (D21). Turn on after enrollment (§4.9) |

Done step: all labels ready, prompt provider valid, every check passed.

### 4.6.1 Public URLs for the tunnel (5 min)

The tunnel stays (Q1). jarvisd answers discovery "in kind": a client that reaches `/services` through a
public hostname gets each service's **public URL**; a client on the LAN gets the LAN URL. The mobile
app always asks `?style=external`, which also returns the public URL where one is set, so phones use the
tunnel on the LAN too, as they did with the legacy registry. The wizard has no step for this, so set
the URLs on the admin **Connections** page right after Done: on each listener, the pencil next to
"no public URL". The value is a base URL (scheme, host, optional port, no path). It is stored in the
registry, and jarvisd's startup sync never overwrites it.

Take the hostnames from `$BK/legacy-services.txt` (§3.4). Drop the legacy `:443`; it is implied.

| Row on Connections | Public URL | Tunnel target (unchanged) |
|---|---|---|
| jarvis-config-service | `https://<config hostname>` | `localhost:7700` |
| jarvis-auth | `https://<auth hostname>` | `localhost:7701` |
| jarvis-logs | `https://<logs hostname>` | `localhost:7702` |
| jarvis-command-center | `https://<command-center hostname>` | `localhost:7703` |
| jarvis-notifications | `https://<notifications hostname>` | `localhost:7712` |
| jarvis-ocr-service | `https://<ocr hostname>` | `localhost:7031` |
| jarvis-mqtt-broker | `wss://<mqtt hostname>` (legacy `wss …:443`) | `localhost:9883`, jarvisd's MQTT WebSocket port |

- The config-service row is required: the public host a client connects to is how jarvisd knows the
  request came through the tunnel. Any configured public host counts, so a client that discovers
  through the command-center hostname is also answered in kind.
- A row without a public URL keeps its LAN URL even through the tunnel. The legacy registry had no public
  name for LLM (7704), whisper (7706) or TTS (7707), so leave them empty. They are tunnelled, but nodes
  reach them through CC.
- The broker accepts only `mqtt`, `mqtts`, `ws` or `wss`; the HTTP services accept only `http` or
  `https`. Anything else is refused with a 422.
- Check through the tunnel: `curl -s https://<config hostname>/services | jq -r '.services[]|"\(.name) \(.url)"'`
  should print the public URLs (`https://…:443`, `wss://…:443`). `curl -s localhost:7700/services` should
  still print `http://localhost:<port>`.

### 4.7 Accounts and households (10 min for the superuser, then users at their pace)

Legacy had 10 users / 8 households / 10 memberships; the two 2-member households are the kitchen node's
and the dormant living-room node's. jarvisd has one superuser and "My Home" now.

```sh
read -r EMAIL; stty -echo; read -r PW; stty echo     # the superuser's credentials
J=$(curl -s localhost:7701/auth/login -H 'content-type: application/json' \
    -d "$(jq -n --arg e "$EMAIL" --arg p "$PW" '{email:$e,password:$p}')" | jq -r .access_token)
curl -s localhost:7701/households -H "authorization: Bearer $J" | jq     # [{id, name:"My Home", role:"admin"}]
```

1. Rename "My Home" to the kitchen household's name (app, or `PATCH /households/{id}` `{"name": …}`).
2. Create the demo node's household if it should stay separate (`POST /households {"name": …}`), as in legacy.
3. Invite the kitchen household's second member: `POST /households/{id}/invites` → an invite code they use
   at sign-up in the app.
4. Every other user signs up again in the app (their own household, as before).
5. Second superuser (legacy had 2): Q9.

Access tokens from legacy fail against jarvisd (new signing key), so apps land on the sign-in screen.

### 4.8 Nodes: re-point without a factory reset (5 min each, ~1 min of it waiting)

For each node: register its **existing** `node_id` on jarvisd, give it the new key, restart it. The node
keeps its Wi-Fi, room, packages and local secrets; it fetches fresh per-node MQTT credentials itself
(A10: 55 s from restart to MQTT on a Pi Zero).

**Kitchen Pi.**

```sh
# On prod: provisioning token for the node's own id (10-minute TTL), then register it.
HH=<kitchen household id from §4.7>; NODE=<node_id from §3>
TOK=$(curl -s -X POST localhost:7703/api/v0/provisioning/token -H "authorization: Bearer $J" \
      -H 'content-type: application/json' -d "$(jq -n --arg h "$HH" --arg n "$NODE" '{household_id:$h,node_id:$n,room:"kitchen"}')" | jq -r .token)
KEY=$(curl -s -X POST localhost:7703/api/v0/nodes/register -H 'content-type: application/json' \
      -d "$(jq -n --arg n "$NODE" --arg t "$TOK" '{node_id:$n,provisioning_token:$t,room:"kitchen"}')" | jq -r .node_key)
[ -n "$KEY" ] && [ "$KEY" != null ] && echo registered    # 201; "Node already registered" = done before
```

```sh
# On the node (ssh pi@<kitchen-node>.local). Prefix with sudo if config.json is root-owned (§3).
CFG=/opt/jarvis-node/config.json
cp -p $CFG $CFG.pre-jarvisd                          # keep: rollback restores it
jq --arg k "<KEY>" --arg h "<HH>" '.api_key=$k | if has("household_id") then .household_id=$h else . end' $CFG > /tmp/cfg.new \
  && cat /tmp/cfg.new > $CFG && rm /tmp/cfg.new     # in place: owner and mode unchanged
sudo systemctl restart <unit from §3>                # or, as in A10: kill "$(systemctl show -p MainPID --value <unit>)"
```

Pass `KEY` to the node without leaving it in shell history or logs (paste it into the jq command; don't
echo it). Expected within ~60 s: the node is refused by the broker once, fetches its MQTT credentials,
connects. On prod, `journalctl -u jarvisd --since -2min | grep -i <first 8 of node_id>` shows its MQTT
connect, and the app's node list shows it online.

**Demo node (container on prod).** Same token and register calls with its node_id, its household and
room `default`, then:

```sh
C=~/jarvis-node/config/config.json
sudo cp -p $C $C.pre-jarvisd
sudo jq --arg k "<KEY>" --arg h "<HH>" '.api_key=$k | if has("household_id") then .household_id=$h else . end' $C | sudo tee /tmp/demo.new >/dev/null \
  && sudo sh -c "cat /tmp/demo.new > $C && rm /tmp/demo.new"
docker update --restart=unless-stopped jarvis-demo-node && docker start jarvis-demo-node
docker logs --since 2m jarvis-demo-node | tail -20    # MQTT connected, no 401s
```

The demo node runs with `JARVIS_CONFIG_URL_STYLE=remote`. If its config URL is a public hostname, it goes
through the tunnel and gets the public URLs (§4.6.1). If its config URL is the LAN IP, it gets LAN URLs:
`remote` keeps its LAN meaning.

**Not migrated now:** the dormant living_room node (last seen 2026-09-02) and four older nodes. When one
comes back it will retry against jarvisd with its old key (A10 F17: WARN lines, no harm); re-point it
the same way then (Q4). The test/canary rows are dropped.

### 4.9 Phones (each user, 5 min)

On the LAN the app finds jarvisd by mDNS (`_jarvis-config._tcp`) or by the manual config URL
`http://10.0.0.107:7700`. Off the LAN, use the config service's public hostname. Either way, once §4.6.1
is done, the app is handed the public URLs, so it keeps working when the phone leaves the house. Each user: sign out, sign up (with the invite code for the kitchen household's
second member), allow notifications so the app registers its push token, pick their node for chat.
Optional voice enrollment, then the superuser turns speaker recognition on (admin Settings,
`voice.recognition_enabled`; threshold 0.43 default, not prod's 0.49).

Lost and re-entered by hand: memories (49), routines (2; default routines seed themselves per household,
D44), phone contacts (2) and per-user call context, devices/rooms (7/8), household location.

### 4.10 Phone calls (Twilio, AD6) (10 min; only if phone stays, Q6)

Prod has phone calls on (17 calls, last 2026-08-30) with Twilio credentials in the gateway's environment.
On jarvisd: per-household settings with a system default and an env fallback; the app's write-only
Twilio section is not built yet (EXTERNAL-CHANGES), so set the **system default** in the admin
(Settings, cc `phone.twilio_account_sid`, `phone.twilio_auth_token`, `phone.twilio_from_number`, E.164),
or as `TWILIO_*` in `/etc/jarvisd/jarvisd.env`.

Public ingress: jarvisd serves the Media Streams WebSocket on the **CC listener** (7703) at
`/phone/media/`; the legacy gateway was 7713.

1. `sudoedit /etc/jarvisd/jarvisd.env`: `JARVIS_PHONE_PUBLIC_URL=https://<hostname that maps to 7703>`
   (and `JARVIS_PHONE_PUBLIC_WSS_URL` only if it differs), then `sudo jarvisd service restart`.
2. If the phone hostname should stay the old one: in `~/.cloudflared/jarvis-services.yml` change its
   `service: http://localhost:7713` to `http://localhost:7703`, then
   `systemctl --user restart cloudflared-jarvis` (backup from §3.4).
3. Turn `phone_calls.enabled` on (admin Settings; prod had it on system-wide).

### 4.11 The Cloudflare tunnel

Leave `cloudflared-jarvis` running (Q1). Its targets are `localhost:<port>`, and jarvisd answers on the
same ports, so anything holding a full public URL still reaches jarvisd, for example a node's cached CC
URL or a phone off the LAN. Discovery through a public hostname returns the public URLs entered in
§4.6.1.

How jarvisd tells: the request's `Host` matches a configured public hostname (Cloudflare passes the
visitor's `Host` through). From a loopback peer (cloudflared itself) only, `X-Forwarded-Host` or
`CF-Connecting-IP` also count, in case an ingress rule sets `httpHostHeader`. The only effect is which of
the two URLs is returned, and `?style=external` returns the public one to anyone anyway, so nothing
security-related depends on these headers.

Ingress changes, in `~/.cloudflared/jarvis-services.yml` (backup in §3.4), then
`systemctl --user restart cloudflared-jarvis`:

- **Phone media:** jarvisd serves Twilio Media Streams on the CC listener at `/phone/media/`. The legacy
  gateway used 7713. Point the phone hostname at `http://localhost:7703` (§4.10 step 2), or use the CC
  hostname in `JARVIS_PHONE_PUBLIC_URL`. Either way 7713 has nothing behind it.
- **Dead targets:** hostnames for 7708 (settings-server), 7713 (unless re-pointed), 7722 (jarvis-web) and
  7030 (recipes) now return errors. Remove them, or leave them until the rollback window closes.
- **Exposure:** the tunnel still publishes 7702 logs, 7704 LLM, 7710 admin and SSH, as Q1 noted. Nothing
  in this runbook needs them from off the LAN. Dropping them is a separate tidy-up.

---

## 5. Verification (20 min)

| # | Test | Pass |
|---|---|---|
| V1 | `sudo jarvisd doctor` | every check OK, exit 0 |
| V2 | Voice turn on the kitchen node: "Hey Jarvis, what's the capital of France?" | spoken answer; Traces page shows the turn **ok**, source the kitchen node, CC time ~0.5–1 s (A10: 462 ms on Qwen3-8B; the 27B is slower) |
| V3 | Voice turn with a tool: "what's the weather" | node tool runs, answer uses it |
| V4 | Demo node (no microphone): online in the app's node list, its tools reported; an app chat routed through it | online; chat answers |
| V5 | App chat from a phone in the kitchen household | reply in a few seconds; Traces shows it |
| V6 | Vision: send a photo in app chat | answered (live has the mmproj) |
| V7 | Push: `curl -s localhost:7712/api/v0/tokens/me -H "authorization: Bearer $J"` lists the phone; then a real push (a reminder by voice, or `POST /api/v0/notify` with an app client from the Connections page, `{"target_type":"user","target_id":"<user id from /auth/me>","title":"cutover","body":"test"}`) | notification arrives on the phone; inbox shows it |
| V8 | Routine: run a seeded routine from the app ("run now") | it runs on the node within ~60 s |
| V9 | Background label: anything that queues a background job (memory extraction after a few turns) | no errors in Logs; GPU 0 shows activity |
| V10 | Phone (if kept): call a contact from the app or by voice | call connects, two-way audio |
| V11 | Reboot test, if the window allows: `sudo reboot` | jarvisd back by itself (`service status`), legacy containers stay down (`docker ps`), nodes reconnect |
| V12 | `nvidia-smi` | two llama-server (one per GPU) + whisper-server; no legacy processes |
| V13 | Off-LAN discovery: `curl -s https://<config hostname>/services \| jq -r '.services[]\|"\(.name) \(.url)"'`, then a phone on mobile data (Wi-Fi off): app chat | the §4.6.1 rows print `https://<hostname>:443` / `wss://<mqtt hostname>:443`; the phone chats over mobile data |

---

## 6. Rollback (10 min)

When: any §2 no-go trigger, or the user asks. Legacy data was never modified (stopped, not removed).

```sh
BK=/mnt/fast/jarvis-legacy-<date>
# 1. jarvisd off, and off at boot too (stop alone leaves the unit enabled).
sudo jarvisd service stop && sudo systemctl disable jarvisd      # or: sudo jarvisd service uninstall (keeps /var/lib/jarvisd)
ss -ltnH '( sport = :7700 or sport = :1884 )'                     # empty

# 2. Restart policies back exactly as recorded, then start what was running.
while read -r name policy state; do docker update --restart="$policy" "$name" >/dev/null; done < "$BK/containers.txt"
docker start $(awk '$3 == "running" {print $1}' "$BK/containers.txt")   # not ollama / minio-init
systemctl --user enable --now jarvis-admin.service    # --stop-legacy disabled it; 7711 back

# 3. Check.
docker ps --format '{{.Names}} {{.Status}}' | sort                # every line "Up"; healthy within ~2 min
curl -s localhost:7700/health; nvidia-smi                         # llama-server + llama-server-bg loaded again (~1–2 min)
```

4. Nodes: put each config back and restart (A10 revert): on the kitchen node
   `cp -p /opt/jarvis-node/config.json.pre-jarvisd /opt/jarvis-node/config.json` and restart the unit; the
   demo node `sudo cp -p ~/jarvis-node/config/config.json.pre-jarvisd ~/jarvis-node/config/config.json && docker restart jarvis-demo-node`.
   They reconnect with their legacy keys.
5. Tunnel: if §4.10 changed it, `cp -p $BK/jarvis-services.yml ~/.cloudflared/ && systemctl --user restart cloudflared-jarvis`.
6. Phones: sign in with the legacy accounts (sessions older than 14 days need the password).
7. Leave `/var/lib/jarvisd` in place for the post-mortem.

If the legacy volumes were somehow damaged, `pg_dumpall-t0.sql.gz` restores into a fresh
`jarvis-postgres` (`gunzip -c … | docker exec -i jarvis-postgres psql -U jarvis -d postgres`).

---

## 7. Post-cutover monitoring

**First 2 hours:** admin Dashboard and Logs (filter ERROR); `journalctl -u jarvisd -f`; Traces page: every
voice turn ok; `nvidia-smi` steady (no engine restarts: `sudo jarvisd service status` restarts 0).

**Day 1:**

- Every user signed up again; kitchen household has both members; push tokens ≥ the number of phones.
- Voice turns per day in the same range as legacy (13 transcripts in the 7 days before).
- No WARN storm from a forgotten node (F17); if one appears, re-point that node or tell its owner.
- Memory extraction running on background (Logs, cc module).
- Disk: `df -h /` and `du -sh /var/lib/jarvisd` (expect ~22 GB + DB).

**Week 1:**

- `jarvisd upgrade --check` works against the real releases; the first real upgrade goes through the
  health gate (snapshots in `/var/lib/jarvisd/backups`).
- `unattended-upgrades` is on: an NVIDIA driver update needs a reboot before CUDA engines load again;
  watch for engines failing after one.
- Speaker recognition turned on after enrollment; check false "unknown speaker" refusals.
- Phone (if kept): one real call end to end (STATUS: first real Twilio call happens at cutover).

**After two stable weeks (only with the user's OK, Q10):** remove the legacy containers and images, the
`jarvis-admin` and `cloudflared-jarvis` user units if unused, and decide about `~/.jarvis/compose/.models`
(150 GB) and the `ollama` volume (16 GB). Keep `$BK` for longer.

---

## 8. Open questions for the user

Things the read-only survey could not settle. Each has a recommendation.

| # | Question | Recommendation |
|---|---|---|
| Q1 | **Off-LAN access.** Prod's registry sends most clients to public HTTPS hostnames through `cloudflared-jarvis`. jarvisd's `/services` answers `http://<host>:<port>` for its own listeners and has no per-service public base URL, so phones that discover through a public hostname break off the LAN. Accept LAN-only at cutover, or add a "public URL per listener" to jarvisd's registry first? Also: the tunnel publishes 7702 logs, 7704 LLM, 7710 admin and SSH to the internet. | LAN-only at cutover (core principle: private by default); keep the tunnel only for the phone media path; revisit remote access as its own feature. |
| Q2 | **Recipes** (48 recipes, `jarvis-recipes-server` 7030) stops with the stack. Bringing it back needs its EXTERNAL-CHANGES work (OCR over HTTP, auth against jarvisd, new user ids). OK to leave it down? | Down at cutover; recipes later as the add-on. Export from the dump if anyone wants them. |
| Q3 | **Browser chat** (`jarvis-web`, 7722) and the deprecated settings-server (7708) go away. OK? | Yes. |
| Q4 | The **living_room** node (0.3.1, last seen 2026-09-02): unplugged on purpose, or should it be re-pointed when it's back? Same for the four older nodes. | Re-point on return with §4.8; ignore the rest. |
| Q5 | Does the kitchen node's **0.3.1** include the "before cutover" node changes (`/node/llm/chat`, per-node MQTT)? (Nodes were not SSH'd into.) | Check the tag; update the node first if not. |
| Q6 | **Phone calls**: keep at cutover (needs Twilio settings + tunnel change, §4.10) or turn on later? | Later, as a follow-up, unless someone relies on it. |
| Q7 | **go2rtc** (cameras, deferred D29) is in the legacy compose project `jarvis`, so `--stop-legacy` now stops it with the rest (nothing on jarvisd uses it; the legacy CC that did is stopped too). Start it again after the install (§4.2), or leave it off? | Leave it off; nothing consumes it until cameras land. |
| Q8 | **Privacy step** values not read from prod: reader proxy (`web_scraping.allow_external`) and update checks. | Update checks on (needed for `jarvisd upgrade --check` in the admin); reader proxy as prod had it (tell me). |
| Q9 | Legacy had **2 superusers**. Does the second one need superuser on jarvisd? (Needs `jarvisd admin-token create auth` + `PUT /admin/users/{id}/superuser`, ID4.) | Only if they use the admin. |
| Q10 | When may the legacy stack be **removed** (containers, 150 GB of models, 16 GB ollama volume)? | Two stable weeks, then ask again. |
| Q11 | Memories (49) and inbox (100) are lost under ID6 (ID6 counted 2 memories; it is 49 now). Re-confirm clean start, or hand-copy a few memories? | Clean start as decided; the dump keeps them. |


## Answers (2026-10-07)

- Q11: clean start re-confirmed by the user with the corrected counts.
- Q1: the Cloudflare tunnel **stays**. Off-LAN service discovery through it needs jarvisd to hand out public URLs. **Built (2026-10-07):** a public URL per registry row (admin Connections), answered "in kind" by `/services` and `/services/{name}`. Entered at §4.6.1; tunnel ingress changes are in §4.11.
- Q5 (checked 2026-10-08 against the node-setup repo): **v0.3.1 does NOT have the cutover node changes.** It has per-node MQTT credentials and `NodeLLMClient`, but `chat_text()`/`chat()` still call `/api/v0/chat` (jarvisd drops it, D5) — that's open PR #134 — and inline routine definitions (D24) are open PR #135. Go/no-go G3 needs both merged, a node-setup release (v0.3.2), and the kitchen + demo nodes updated before cutover. Without them: jokes, routine briefings and "what's up" fail, and server-sent routine definitions don't run.
- Q2 (user 2026-10-08): **recipes must work at cutover** — "we need to get everything working at the same time so we can delay deploying". Cutover waits for one coordinated release: jarvisd + the recipes add-on on jarvisd (OCR over HTTP + callback, auth against jarvisd) + node-setup v0.3.2 (#134, #135) + the mobile Twilio section. Recipe ownership: **remap by email** with a one-time recipes-side script run after users re-register (legacy user id → new jarvisd id; unmatched rows stay orphaned for later cleanup).
