#!/usr/bin/env python3
"""Seed a jarvisd dev server with realistic demo data: logins, household, nodes, content.

Rebuilds a dev install that looks lived-in: real users you can log into the admin UI and the
mobile app with, a household with rooms and devices, two registered nodes that genuinely
authenticate, plus voice history, memories, routines, a phonebook and live signals. Use it on
a fresh database to take screenshots, or to get a working dev environment back in minutes.
It is the jarvisd port of the legacy stack's scripts/seed_dev.py (umbrella repo), with the
same personas, ids and content.

Python 3 standard library only (urllib, sqlite3), so it runs anywhere without a venv.

Two layers, deliberately different:

  * IDENTITY + NODES go through jarvisd's REAL APIs: /auth/setup (with the first-run setup
    token), /auth/login, /households, invites + /auth/register, and the app's provisioning
    flow (/api/v0/provisioning/token, then /api/v0/nodes/register). Passwords get properly
    hashed and node keys are minted and registered by jarvisd, so a seeded login is a real
    login and a seeded node really authenticates. No ADMIN_API_KEY or admin token is needed.
  * CONTENT (rooms, devices, phone contacts, routines, memories, transcripts, signals) is
    written straight into <home>/jarvis.db in ONE transaction. The APIs mint their own ids
    and stamp everything "now"; the seeder needs fixed ids and seed markers (so reruns and
    --reset are exact) and back-dated timestamps (so history looks real). jarvisd runs the
    database in WAL mode, so this is safe while the server is up.

Every run REPLACES the seeded content (it deletes the rows carrying the seed markers and
inserts them again), so it is idempotent, and timestamps stay relative to "now": run it again
right before taking screenshots and the history is recent. Identity and nodes are reused.
Transcripts span 14 days; jarvisd prunes those past memory.transcript_ttl_days (7) daily.

Personas are PSEUDONYMOUS on purpose: dev data flows into prompts and can leave the machine.
The shape mirrors a real household (two adults, a child, two dogs on medication, kitchen +
living room nodes), plus a relative with power-user rights so every household role has a login;
the names do not.

PREREQUISITES

  1. jarvisd is running, ideally on a fresh database (stop it, move <home>/jarvis.db* aside,
     start it again). The seeder can finish first-run setup itself: it reads the setup token
     from <home>/setup-token (what `jarvisd setup-link` prints), or take --setup-token.
     Do this INSTEAD of the browser wizard's account step; the rest of the wizard (hardware,
     models, privacy) still runs when you sign in to the admin afterwards.
  2. It runs as an account that can write <home>. On a Linux system install that is the
     jarvisd user:

        sudo -u jarvisd python3 scripts/seed-dev.py --home /var/lib/jarvisd

     (the script must be readable by that user: copy it to /tmp first if your checkout
     isn't). For a per-user install the home is ~/.jarvisd and no sudo is needed.

USAGE

    scripts/seed-dev.py --home ~/.jarvisd                    # rich profile (default)
    scripts/seed-dev.py --home ~/.jarvisd --profile minimal  # identity + topology only
    scripts/seed-dev.py --home ~/.jarvisd --reset            # delete the seeded content, stop
    scripts/seed-dev.py --home DIR --base-url http://127.0.0.1 --auth-port 27701 --cc-port 27703

Ports default to JARVIS_PORT_AUTH / JARVIS_PORT_COMMAND_CENTER / JARVIS_PORT_ADMIN when set,
else jarvisd's legacy ports (7701, 7703, 7710).

Node keys are stored only as hashes on the server, so the seeder keeps the plaintext keys it
minted in <home>/seed-dev-state.json (0600) and reuses them on later runs: the printed node
config stays valid across reruns. If that file is lost, the seeded nodes are re-created with
new keys (their rows are deleted directly, then provisioned again through the API).
"""

from __future__ import annotations

import argparse
import json
import os
import random
import re
import sqlite3
import sys
import time
from contextlib import closing
import urllib.error
import urllib.request
from datetime import datetime, timedelta, timezone

# ── Fixed identifiers ─────────────────────────────────────────────────────────
# Deterministic so reruns are idempotent and the node config stays valid across rebuilds.
DEV_NODE_ID = "5eed0000-0000-4000-8000-00000000a0de"
LIVING_NODE_ID = "5eed0000-0000-4000-8000-00000000117e"

HOUSEHOLD_NAME = "Avery Household"
DEFAULT_PASSWORD = "DevPass123!"
PRIMARY_EMAIL = "test@jarvisautomation.io"
PARTNER_EMAIL = "carl@jarvisautomation.io"

PEOPLE = [
    # (email, username, display, role, is_primary)
    (PRIMARY_EMAIL, "jordan", "Jordan", "admin", True),
    (PARTNER_EMAIL, "carl", "Carl", "admin", False),
    ("sam@jarvisautomation.io", "sam", "Sam", "member", False),
    # A relative who helps run the house: a power user, the role between member and admin.
    ("robin@jarvisautomation.io", "robin", "Robin", "power_user", False),
]

ROOMS = [
    # (name, legacy normalized name, icon); jarvisd normalizes room names as lower(trim(name)).
    ("Kitchen", "kitchen", "chef-hat"),
    ("Living Room", "living_room", "sofa"),
    ("Bedroom", "bedroom", "bed"),
    ("Office", "office", "desk"),
]

STATE_FILE = "seed-dev-state.json"

# Seed markers: what --reset and every rerun delete. Specific enough that nothing unseeded
# can match, and deliberately not scoped by household (an earlier run against another
# household must not leave invisible duplicates behind).
SEED_MARKERS = (
    ("cc_signals", "source_agent = 'seed'"),
    ("cc_conversation_transcripts", "conversation_id LIKE 'seed-%'"),
    ("cc_user_memories", "source = 'seed'"),
    ("cc_routines", "id LIKE '5eed0003-%'"),
    ("cc_phone_contacts", "id LIKE '5eed0002-%'"),
    ("cc_devices", "id LIKE '5eed0001-%'"),
    ("cc_rooms", "id LIKE '5eed0000-%'"),
)


def log(msg: str, indent: int = 0) -> None:
    print(f"{'  ' * indent}{msg}", flush=True)


class SeedError(Exception):
    pass


# ── HTTP (stdlib only) ────────────────────────────────────────────────────────
def request(method: str, url: str, body: dict | None = None, headers: dict | None = None,
            timeout: float = 20) -> tuple[int, object]:
    data = json.dumps(body).encode() if body is not None else None
    hdrs = {"Content-Type": "application/json", **(headers or {})}
    req = urllib.request.Request(url, data=data, headers=hdrs, method=method)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            raw = resp.read().decode() or "{}"
            return resp.status, json.loads(raw)
    except urllib.error.HTTPError as e:
        raw = e.read().decode() or "{}"
        try:
            return e.code, json.loads(raw)
        except json.JSONDecodeError:
            return e.code, {"detail": raw[:200]}
    except (urllib.error.URLError, OSError, ValueError) as e:
        return 0, {"detail": str(e)}


def detail(body: object) -> str:
    if isinstance(body, dict) and "detail" in body:
        return str(body["detail"])
    return json.dumps(body)[:200]


def wait_for(url: str, name: str, timeout_s: int = 60) -> None:
    deadline = time.monotonic() + timeout_s
    while True:
        status, _ = request("GET", url, timeout=5)
        if status == 200:
            log(f"ok  {name} healthy", 1)
            return
        if time.monotonic() >= deadline:
            raise SeedError(f"{name} not healthy at {url} after {timeout_s}s; is jarvisd running?")
        time.sleep(1)


def bearer(token: str) -> dict:
    return {"Authorization": f"Bearer {token}"}


def login(auth_url: str, email: str, password: str) -> tuple[int, dict]:
    status, body = request("POST", f"{auth_url}/auth/login", {"email": email, "password": password})
    return status, body if isinstance(body, dict) else {}


# ── Layer 1: identity (real APIs, real password hashes) ───────────────────────
def read_setup_token(home: str, flag: str) -> str:
    if flag:
        return flag
    if os.environ.get("JARVIS_SETUP_TOKEN"):
        return os.environ["JARVIS_SETUP_TOKEN"]
    path = os.path.join(home, "setup-token")
    try:
        with open(path, encoding="utf-8") as f:
            return f.read().strip()
    except FileNotFoundError:
        raise SeedError(f"jarvisd needs first-run setup but {path} does not exist.\n"
                        "   Is --home the running server's home? `jarvisd setup-link` prints the "
                        "token; pass it with --setup-token.") from None
    except PermissionError:
        raise SeedError(f"cannot read {path}: run as the jarvisd account "
                        "(sudo -u jarvisd ...) or pass --setup-token.") from None


def seed_identity(auth_url: str, home: str, password: str, setup_token: str) -> dict:
    """Create the superuser, name the household, and add the other members.

    Idempotent: once setup is done /auth/setup-status says so and everything falls through to
    logins and membership checks.
    """
    log("-- identity --")
    email, username, display, _, _ = PEOPLE[0]

    status, body = request("GET", f"{auth_url}/auth/setup-status")
    if status != 200:
        raise SeedError(f"/auth/setup-status failed: {status} {detail(body)}")
    if body.get("needs_setup"):
        tok = read_setup_token(home, setup_token)
        status, body = request("POST", f"{auth_url}/auth/setup",
                               {"email": email, "username": username, "password": password},
                               {"X-Jarvis-Setup-Token": tok})
        if status not in (200, 201):
            raise SeedError(f"/auth/setup failed: {status} {detail(body)}")
        log(f"created superuser {email}", 1)
    else:
        log("setup already done; reusing the accounts", 1)

    status, body = login(auth_url, email, password)
    if status != 200:
        raise SeedError(f"cannot log in as {email}: {status} {detail(body)}\n"
                        "   If this server was set up by hand or with another password, seed a "
                        "fresh database or pass --password.")
    token = body["access_token"]
    log(f"logged in as {display}", 1)

    # Household: setup made "My Home"; reuse the one Jordan administers and give it the demo
    # name. Never create a second one on a rerun, or the content would be split across two.
    status, households = request("GET", f"{auth_url}/households", headers=bearer(token))
    if status != 200 or not isinstance(households, list):
        raise SeedError(f"GET /households failed: {status} {detail(households)}")
    admin_of = [h for h in households if h.get("role") == "admin"]
    named = [h for h in admin_of if h.get("name") == HOUSEHOLD_NAME]
    pick = (named or admin_of or [None])[0]
    if pick is None:
        status, body = request("POST", f"{auth_url}/households", {"name": HOUSEHOLD_NAME}, bearer(token))
        if status not in (200, 201):
            raise SeedError(f"could not create the household: {status} {detail(body)}")
        household_id = body["id"]
        log(f"created household {household_id}", 1)
    else:
        household_id = pick["id"]
        if pick.get("name") != HOUSEHOLD_NAME:
            status, body = request("PATCH", f"{auth_url}/households/{household_id}",
                                   {"name": HOUSEHOLD_NAME}, bearer(token))
            if status != 200:
                raise SeedError(f"could not rename the household: {status} {detail(body)}")
            log(f"renamed household {household_id} to {HOUSEHOLD_NAME!r}", 1)
        else:
            log(f"reusing household {household_id}", 1)

    members = list_members(auth_url, household_id, token)
    for email_i, username_i, display_i, role, _ in PEOPLE[1:]:
        if email_i not in members:
            add_member(auth_url, household_id, token, email_i, username_i, display_i, password)
            members = list_members(auth_url, household_id, token)
            if email_i not in members:
                raise SeedError(f"{display_i} is still not a member of {household_id}")
        else:
            log(f"{display_i} already a member", 1)
        uid, current = members[email_i]
        if current != role:
            # Invites can't grant admin, so promote afterwards (the household admin may).
            status, body = request("PATCH", f"{auth_url}/households/{household_id}/members/{uid}",
                                   {"role": role}, bearer(token))
            if status != 200:
                raise SeedError(f"could not make {display_i} {role}: {status} {detail(body)}")
            log(f"{display_i}: {current} -> {role}", 1)

    members = list_members(auth_url, household_id, token)
    user_ids = {e: uid for e, (uid, _) in members.items()}
    return {"household_id": household_id, "token": token, "user_ids": user_ids}


def list_members(auth_url: str, household_id: str, token: str) -> dict[str, tuple[int, str]]:
    status, body = request("GET", f"{auth_url}/households/{household_id}/members", headers=bearer(token))
    if status != 200 or not isinstance(body, list):
        raise SeedError(f"GET members failed: {status} {detail(body)}")
    return {m["email"]: (int(m["user_id"]), m["role"]) for m in body}


def add_member(auth_url: str, household_id: str, token: str, email: str, username: str,
               display: str, password: str) -> None:
    status, body = request("POST", f"{auth_url}/households/{household_id}/invites",
                           {"default_role": "member", "max_uses": 1, "expires_in_days": 1}, bearer(token))
    if status not in (200, 201):
        raise SeedError(f"could not create an invite for {display}: {status} {detail(body)}")
    code = body["code"]
    payload = {"email": email, "username": username, "password": password, "invite_code": code}
    for attempt in range(2):
        status, body = request("POST", f"{auth_url}/auth/register", payload)
        if status != 429:
            break
        log(f"rate limited creating {display}; retrying once", 1)
        time.sleep(5)
    if status in (200, 201):
        log(f"created {display} ({email})", 1)
        return
    if status == 400 and "already registered" in detail(body):
        # The account exists but isn't in this household: join with the invite as that user.
        s, b = login(auth_url, email, password)
        if s != 200:
            raise SeedError(f"{email} exists but cannot log in: {s} {detail(b)}")
        s, b = request("POST", f"{auth_url}/households/join", {"invite_code": code}, bearer(b["access_token"]))
        if s not in (200, 201):
            raise SeedError(f"{display} could not join the household: {s} {detail(b)}")
        log(f"{display} existed; joined the household", 1)
        return
    raise SeedError(f"could not create {display}: {status} {detail(body)}")


# ── Layer 2: topology (nodes via the app's provisioning flow) ─────────────────
def load_state(home: str) -> dict:
    try:
        with open(os.path.join(home, STATE_FILE), encoding="utf-8") as f:
            data = json.load(f)
        return data if isinstance(data, dict) else {}
    except (FileNotFoundError, json.JSONDecodeError):
        return {}


def save_state(home: str, state: dict) -> None:
    path = os.path.join(home, STATE_FILE)
    tmp = path + ".tmp"
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as f:
        json.dump(state, f, indent=2, sort_keys=True)
        f.write("\n")
    os.replace(tmp, path)


def node_authenticates(cc_url: str, node_id: str, key: str) -> bool:
    # /api/v0/node/devices is node-authenticated: 200 only with a valid key. /health is open,
    # so it would not prove anything.
    status, _ = request("GET", f"{cc_url}/api/v0/node/devices", headers={"X-API-Key": f"{node_id}:{key}"})
    return status == 200


def forget_node(db_path: str, node_id: str) -> None:
    """Delete a seeded node's rows so it can be provisioned again with a fresh key.

    Only reached for the fixed seed node ids whose key the seeder has lost. The app's DELETE
    route would also publish a factory-reset to a real node using that id, so it is avoided.
    """
    with closing(open_db(db_path)) as cx:
        cx.execute("BEGIN IMMEDIATE")
        cx.execute("DELETE FROM cc_nodes WHERE node_id = ?", (node_id,))
        cx.execute("DELETE FROM auth_node_registrations WHERE node_id = ?", (node_id,))
        cx.execute("COMMIT")


def seed_nodes(cc_url: str, token: str, household_id: str, node_id: str, home: str,
               db_path: str) -> dict[str, str]:
    log("-- nodes --")
    state = load_state(home)
    saved = state.get("nodes", {})
    keys: dict[str, str] = {}
    for nid, room, name in (
        (node_id, "kitchen", "dev-kitchen"),
        (LIVING_NODE_ID, "living_room", "dev-living-room"),
    ):
        status, _ = request("GET", f"{cc_url}/api/v0/admin/nodes/{nid}", headers=bearer(token))
        if status == 200:
            key = saved.get(nid, "")
            if key and node_authenticates(cc_url, nid, key):
                keys[nid] = key
                log(f"reusing {name} ({room})", 1)
                continue
            log(f"{name} exists but its key is unknown here; re-creating it with a new key", 1)
            forget_node(db_path, nid)
        elif status not in (404,):
            # 403: the node exists in another household. Don't touch it.
            raise SeedError(f"GET node {nid}: {status}")

        status, body = request("POST", f"{cc_url}/api/v0/provisioning/token",
                               {"household_id": household_id, "node_id": nid, "room": room, "name": name},
                               bearer(token))
        if status not in (200, 201):
            raise SeedError(f"provisioning token for {name}: {status} {detail(body)}")
        status, body = request("POST", f"{cc_url}/api/v0/nodes/register",
                               {"node_id": nid, "provisioning_token": body["token"], "room": room})
        if status not in (200, 201):
            raise SeedError(f"registering {name}: {status} {detail(body)}")
        keys[nid] = body["node_key"]
        saved[nid] = body["node_key"]
        state["nodes"] = saved
        save_state(home, state)
        log(f"registered {name} ({room})", 1)
    return keys


# ── Layer 3: content (direct SQLite, one transaction) ─────────────────────────
def open_db(path: str) -> sqlite3.Connection:
    if not os.path.exists(path):
        raise SeedError(f"{path} does not exist; is --home the running jarvisd's home?")
    cx = sqlite3.connect(path, timeout=30, isolation_level=None)
    cx.execute("PRAGMA busy_timeout = 30000")
    cx.execute("PRAGMA foreign_keys = ON")
    return cx


def ts(t: datetime) -> str:
    """jarvisd's cc timestamp format (dbTime): UTC, milliseconds, Z."""
    return t.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.") + f"{t.microsecond // 1000:03d}Z"


def contact_norm(name: str) -> str:
    """phone.NormalizeName: lowercase, strip [^a-z0-9 ], trim."""
    return re.sub(r"[^a-z0-9 ]", "", name.lower()).strip()


def slugify(name: str) -> str:
    """cc routines slugify: lowercase, runs of non-alphanumerics -> '_'."""
    return re.sub(r"[^a-z0-9]+", "_", name.strip().lower()).strip("_") or "routine"


def routine_steps(steps: list[dict]) -> list[dict]:
    """Legacy {command, args: {k: v}} -> jarvisd's stored [{command, args: [{key, value}], label}].

    Values are strings; lists/objects are JSON-encoded, as the routine editor stores them.
    """
    out = []
    for s in steps:
        args = [{"key": k, "value": v if isinstance(v, str) else json.dumps(v)}
                for k, v in s.get("args", {}).items()]
        out.append({"command": s["command"], "args": args, "label": s.get("label", "")})
    return out


def delete_seeded(cx: sqlite3.Connection) -> dict[str, int]:
    removed = {}
    for tbl, where in SEED_MARKERS:
        removed[tbl] = cx.execute(f"DELETE FROM {tbl} WHERE {where}").rowcount
    return removed


def reset_content(db_path: str) -> None:
    log("-- reset --")
    with closing(open_db(db_path)) as cx:
        cx.execute("BEGIN IMMEDIATE")
        removed = delete_seeded(cx)
        cx.execute("COMMIT")
    for tbl, n in removed.items():
        log(f"{tbl}: -{n}", 1)


def seed_content(db_path: str, household_id: str, node_id: str, user_ids: dict[str, int],
                 rng: random.Random, rich: bool) -> dict[str, int]:
    log("-- content --")
    now = datetime.now(timezone.utc)
    stamp = ts(now)
    primary = user_ids[PRIMARY_EMAIL]
    partner = user_ids.get(PARTNER_EMAIL, primary)
    counts: dict[str, int] = {}

    def insert(cx: sqlite3.Connection, sql: str, args: tuple) -> int:
        return cx.execute(sql, args).rowcount

    cx = open_db(db_path)
    mode = cx.execute("PRAGMA journal_mode").fetchone()[0]
    if mode.lower() != "wal":
        log(f"warning: journal_mode is {mode}, not wal; writes may block jarvisd briefly", 1)
    try:
        cx.execute("BEGIN IMMEDIATE")
        delete_seeded(cx)  # replace: reruns stay exact and timestamps move with "now"

        # Rooms -------------------------------------------------------------
        room_ids: dict[str, str] = {}
        n = 0
        for i, (name, legacy_norm, icon) in enumerate(ROOMS):
            rid = f"5eed0000-0000-4000-8000-{i:012d}"
            norm = name.strip().lower()
            n += insert(cx, """INSERT INTO cc_rooms (id, household_id, name, normalized_name, icon,
                created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING""",
                        (rid, household_id, name, norm, icon, stamp, stamp))
            # A room the household already had under that name keeps its id.
            row = cx.execute("SELECT id FROM cc_rooms WHERE household_id = ? AND normalized_name = ?",
                             (household_id, norm)).fetchone()
            room_ids[legacy_norm] = row[0]
        counts["rooms"] = n

        # Devices -----------------------------------------------------------
        devices = [
            ("light.kitchen_ceiling", "Kitchen Ceiling", "light", "kitchen", "lifx"),
            ("light.kitchen_under_cabinet", "Under Cabinet", "light", "kitchen", "lifx"),
            ("kettle.smart_kettle", "Smart Kettle", "kettle", "kitchen", "govee"),
            ("light.living_room_lamp", "Living Room Lamp", "light", "living_room", "lifx"),
            ("climate.thermostat", "Thermostat", "climate", "living_room", "nest"),
            ("camera.front_door", "Front Door", "camera", "living_room", "nest"),
            ("lock.front_door", "Front Door Lock", "lock", "living_room", "schlage"),
            ("light.bedroom_lamp", "Bedroom Lamp", "light", "bedroom", "lifx"),
            ("switch.office_fan", "Office Fan", "switch", "office", "kasa"),
        ]
        n = 0
        for i, (eid, name, domain, room, proto) in enumerate(devices):
            n += insert(cx, """INSERT INTO cc_devices (id, household_id, room_id, entity_id, name, domain,
                protocol, source, is_controllable, is_active, created_at, updated_at)
                VALUES (?, ?, ?, ?, ?, ?, ?, 'seed', 1, 1, ?, ?) ON CONFLICT DO NOTHING""",
                        (f"5eed0001-0000-4000-8000-{i:012d}", household_id, room_ids.get(room), eid, name,
                         domain, proto, stamp, stamp))
        counts["devices"] = n

        # Phonebook ---------------------------------------------------------
        # 555-01xx is the reserved fictional range, so a stray auto-dial can't reach anyone.
        contacts = [
            ("Cedar Street Pharmacy", "+15550100", "pharmacy", "24 Cedar St"),
            ("Dr. Nkemelu Family Practice", "+15550101", "doctor", "8 Field Rd"),
            ("Brightpaw Veterinary", "+15550102", "vet", "150 Mill Ave"),
            ("Nonna's Pizza", "+15550103", "restaurant", "31 Market St"),
            ("Riverside Dental", "+15550104", "dentist", "9 Riverside Dr"),
            ("Meadow Lane School", "+15550105", "school", "2 Meadow Ln"),
            ("Ace Plumbing", "+15550106", "trade", None),
            ("Kwik Auto Service", "+15550107", "trade", "77 Depot St"),
        ]
        n = 0
        for i, (name, number, kind, addr) in enumerate(contacts):
            n += insert(cx, """INSERT INTO cc_phone_contacts (id, household_id, name, normalized_name, number,
                address, source, notes, do_not_call, created_at, updated_at)
                VALUES (?, ?, ?, ?, ?, ?, 'manual', ?, 0, ?, ?) ON CONFLICT DO NOTHING""",
                        (f"5eed0002-0000-4000-8000-{i:012d}", household_id, name, contact_norm(name), number,
                         addr, kind, stamp, stamp))
        counts["phone_contacts"] = n

        # Routines ----------------------------------------------------------
        # Slugged the way jarvisd slugs names, so the lazily seeded node defaults ("Good
        # Morning", "Good Night") yield to these instead of appearing twice.
        routines = [
            ("Good Morning", ["good morning", "start the day"],
             [{"command": "get_weather", "args": {}},
              {"command": "get_calendar_events", "args": {"resolved_datetimes": ["today"]}}],
             "Give a warm short briefing."),
            ("Good Night", ["good night", "bedtime"],
             [{"command": "control_device", "args": {"entity_id": "light.kitchen_ceiling",
                                                     "action": "turn_off"}}],
             "Confirm briefly and wish them a good night."),
            ("Leaving Home", ["i'm heading out", "leaving now"],
             [{"command": "control_device", "args": {"entity_id": "lock.front_door",
                                                     "action": "lock"}}],
             "Confirm what was secured in one line."),
        ]
        n = 0
        for i, (name, phrases, steps, instr) in enumerate(routines):
            n += insert(cx, """INSERT INTO cc_routines (id, household_id, slug, name, trigger_phrases, steps,
                response_instruction, response_length, enabled, created_at, updated_at)
                VALUES (?, ?, ?, ?, ?, ?, ?, 'short', 1, ?, ?) ON CONFLICT DO NOTHING""",
                        (f"5eed0003-0000-4000-8000-{i:012d}", household_id, slugify(name), name,
                         json.dumps(phrases), json.dumps(routine_steps(steps)), instr, stamp, stamp))
        counts["routines"] = n

        if rich:
            counts.update(seed_rich(cx, household_id, node_id, primary, partner, rng, now))
        cx.execute("COMMIT")
    except BaseException:
        if cx.in_transaction:
            cx.execute("ROLLBACK")
        raise
    finally:
        cx.close()

    if not rich:
        log("minimal profile: no memories, transcripts or signals", 1)
    for k, v in counts.items():
        log(f"{k}: {v}", 2)
    return counts


def seed_rich(cx: sqlite3.Connection, household_id: str, node_id: str, primary: int, partner: int,
              rng: random.Random, now: datetime) -> dict[str, int]:
    counts: dict[str, int] = {}
    stamp = ts(now)

    # User memories ---------------------------------------------------------
    # embedding stays NULL: jarvisd's embedding sweep fills it once an embedder is configured.
    memories = [
        (primary, "pets", "Has two golden retrievers, Biscuit and Pepper"),
        (primary, "pets", "Biscuit takes anti-seizure medication twice a day"),
        (primary, "family", "Partner is Carl; they have a toddler named Rowan"),
        (primary, "preferences", "Prefers coffee black, no sugar"),
        (primary, "preferences", "Does not want jokes about work stress"),
        (primary, "home", "Lives in Brookfield"),
        (primary, "food", "Enjoys pizza on Friday nights"),
        (primary, "schedule", "Cuts the grass on Sunday evenings"),
        (partner, "pets", "Biscuit is the older of the two dogs"),
        (partner, "preferences", "Prefers tea in the morning"),
        (partner, "family", "Rowan naps around 1pm"),
        (partner, "schedule", "Works from home on Tuesdays and Thursdays"),
    ]
    for uid, cat, content in memories:
        when = ts(now - timedelta(days=rng.randint(1, 60)))
        cx.execute("""INSERT INTO cc_user_memories (user_id, household_id, category, content, source,
            is_active, is_pinned, created_at, updated_at) VALUES (?, ?, ?, ?, 'seed', 1, 0, ?, ?)""",
                   (uid, household_id, cat, content, when, when))
    counts["user_memories"] = len(memories)

    # Conversation transcripts ----------------------------------------------
    exchanges = [
        ("What's the weather today?", "It's 68 and sunny — nice day for it."),
        ("Turn on the kitchen lights", None),
        ("Biscuit took his medicine", None),
        ("What's on my calendar tomorrow?", "You've got a dentist appointment at 9."),
        ("Set a timer for 10 minutes", None),
        ("Who is Rowan?", "Rowan is your toddler."),
        ("Play something upbeat", None),
        ("Lock the front door", None),
        ("Is the front door locked?", "Yes, it's locked."),
        ("Add milk to the shopping list", None),
        ("How long does it take to get to the vet?", "About 12 minutes right now."),
        ("Good morning", "Morning! It's 64 out and you're clear until noon."),
        ("What did I ask you to remind me about?", "To call the pharmacy back."),
        ("Turn the thermostat down two degrees", None),
        ("Thanks", "Anytime."),
    ]
    n_tx = 0
    for day in range(14):
        for user_msg, assistant in rng.sample(exchanges, rng.randint(2, 5)):
            uid = rng.choice([primary, partner])
            created = now - timedelta(days=day, hours=rng.randint(0, 14))
            cx.execute("""INSERT INTO cc_conversation_transcripts (user_id, household_id, conversation_id,
                user_message, assistant_message, is_processed, processed_at, created_at)
                VALUES (?, ?, ?, ?, ?, 1, ?, ?)""",
                       (uid, household_id, f"seed-{day}-{n_tx}", user_msg, assistant, ts(created), ts(created)))
            n_tx += 1
    counts["conversation_transcripts"] = n_tx

    # Signals ---------------------------------------------------------------
    signals = [
        ("presence.seen", "jordan", "Jordan was heard in the kitchen"),
        ("presence.seen", "carl", "Carl was heard in the living room"),
        ("appt.upcoming", "dentist", "Dentist appointment tomorrow at 9:00 AM"),
    ]
    n = 0
    for i, (kind, subject, summary) in enumerate(signals):
        n += cx.execute("""INSERT INTO cc_signals (household_id, user_id, node_id, room, kind, subject,
            source_key, summary, source_agent, cacheable, is_active, observed_at, created_at, updated_at)
            VALUES (?, ?, ?, 'kitchen', ?, ?, ?, ?, 'seed', 0, 1, ?, ?, ?) ON CONFLICT DO NOTHING""",
                        (household_id, primary, node_id, kind, subject, f"seed:{kind}:{i}", summary,
                         stamp, stamp, stamp)).rowcount
    counts["signals"] = n
    return counts


# ── Verify ────────────────────────────────────────────────────────────────────
def verify(auth_url: str, cc_url: str, password: str, keys: dict[str, str]) -> bool:
    """A green run must mean it actually works, not that inserts succeeded."""
    log("-- verify --")
    ok = True
    for email, _, display, _, _ in PEOPLE:
        status, _ = login(auth_url, email, password)
        log(f"{'ok ' if status == 200 else 'BAD'} login {display} ({email})", 1)
        ok &= status == 200
    for nid, key in keys.items():
        good = bool(key) and node_authenticates(cc_url, nid, key)
        log(f"{'ok ' if good else 'BAD'} node {nid} authenticates to command-center", 1)
        ok &= good
    if not keys:
        log("BAD no node keys: node registration failed", 1)
        ok = False
    return ok


def env_port(name: str, default: int) -> int:
    v = os.environ.get(name, "")
    return int(v) if v.isdigit() else default


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--home", required=True, help="the running jarvisd's data directory (holds jarvis.db)")
    p.add_argument("--base-url", default="http://localhost", help="scheme and host of jarvisd (default %(default)s)")
    p.add_argument("--auth-port", type=int, default=env_port("JARVIS_PORT_AUTH", 7701))
    p.add_argument("--cc-port", type=int, default=env_port("JARVIS_PORT_COMMAND_CENTER", 7703))
    p.add_argument("--admin-port", type=int, default=env_port("JARVIS_PORT_ADMIN", 7710),
                   help="only printed in the summary")
    p.add_argument("--setup-token", default="",
                   help="first-run setup token (default: $JARVIS_SETUP_TOKEN, else <home>/setup-token)")
    p.add_argument("--password", default=os.getenv("SEED_PASSWORD", DEFAULT_PASSWORD),
                   help="password for every seeded user (default %(default)s)")
    p.add_argument("--node-id", default=DEV_NODE_ID, help="kitchen node id (default: the fixed dev one)")
    p.add_argument("--profile", choices=("minimal", "rich"), default="rich")
    p.add_argument("--reset", action="store_true",
                   help="delete the seeded content rows and stop (users, household and nodes stay)")
    p.add_argument("--seed", type=int, default=1337, help="RNG seed (determinism)")
    args = p.parse_args()

    home = os.path.abspath(os.path.expanduser(args.home))
    db_path = os.path.join(home, "jarvis.db")
    base = args.base_url.rstrip("/")
    auth_url = f"{base}:{args.auth_port}"
    cc_url = f"{base}:{args.cc_port}"

    try:
        if args.reset:
            reset_content(db_path)
            log("seeded content removed; rerun without --reset to seed again")
            return 0

        log("Seeding jarvisd dev data")
        log(f"home={home}  auth={auth_url}  cc={cc_url}  profile={args.profile}")
        log("")
        log("-- services --")
        wait_for(f"{auth_url}/health", "auth")
        wait_for(f"{cc_url}/health", "command-center")

        ident = seed_identity(auth_url, home, args.password, args.setup_token)
        household_id = ident["household_id"]
        keys = seed_nodes(cc_url, ident["token"], household_id, args.node_id, home, db_path)
        seed_content(db_path, household_id, args.node_id, ident["user_ids"],
                     random.Random(args.seed), rich=args.profile == "rich")
        ok = verify(auth_url, cc_url, args.password, keys)
    except SeedError as e:
        print(f"FAILED: {e}", file=sys.stderr)
        return 1

    log("")
    log("=" * 66)
    log(f"admin     : {base}:{args.admin_port}")
    log(f"household : {household_id} ({HOUSEHOLD_NAME})")
    log(f"password  : {args.password}   (all users)")
    for email, _, display, role, _ in PEOPLE:
        log(f"  {display:8} {email:34} {role}")
    node_key = keys.get(args.node_id, "")
    if node_key:
        log("")
        log("node config.json snippet for the dev Pi (kitchen):")
        log(json.dumps({"node_id": args.node_id, "api_key": node_key, "command_center_url": cc_url}, indent=2))
    other = keys.get(LIVING_NODE_ID, "")
    if other:
        log(f"living-room node: {LIVING_NODE_ID}  key {other}")
    log("=" * 66)
    log("dev environment ready" if ok else "seeded, but verification FAILED")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
