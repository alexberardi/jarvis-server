"""Export the command-data browser goldens (go test reads internal/agent/sidecar/testdata/
command_data_golden.json).

The desktop agent must answer the mobile app's "Stored data" requests exactly as a Pi does
(DESIGN §16). This runs the REAL Python node code:

  - services/command_data_handler.py (the MQTT handler for jarvis/nodes/{id}/command-data/{op}),
  - repositories/command_data_repository.py on an in-memory SQLite command_data table, and
  - services/storage_backend.py as JarvisStorage's backend,

over the fixture commands in internal/agent/sidecar/testdata/browser (the agent's sidecar loads
the same files), seeds the same records, replays a script of app requests (as command-center
relays them) and records every answer. Only logging, MQTT, the database URL, secrets and
command discovery are stubbed.

    uv run --with sqlalchemy \\
        --with 'jarvis-command-sdk @ git+https://github.com/alexberardi/jarvis-command-sdk@main' \\
        python tools/golden/export_command_data.py ../jarvis-node-setup \\
        > internal/agent/sidecar/testdata/command_data_golden.json

A create through the SDK's default data_browser_create mints a random key: it is written as
"<created:N>" everywhere it appears, and the Go test does the same.
"""
import importlib.util
import inspect
import json
import pathlib
import sys
import types
from datetime import datetime, timezone

NODE = pathlib.Path(sys.argv[1]).resolve()
HERE = pathlib.Path(__file__).resolve().parent
FIXTURES = HERE.parent.parent / "internal/agent/sidecar/testdata/browser"
NODE_ID = "golden-node-1"


class _Any:
    def __getattr__(self, name):
        return _Any()

    def __call__(self, *a, **k):
        return _Any()


def stub(name, path=None, **attrs):
    m = types.ModuleType(name)
    m.__dict__["__getattr__"] = lambda attr: _Any()
    m.__path__ = [str(path)] if path else []
    for k, v in attrs.items():
        setattr(m, k, v)
    sys.modules[name] = m
    return m


sys.path.insert(0, str(NODE))

from sqlalchemy import create_engine  # noqa: E402
from sqlalchemy.orm import sessionmaker  # noqa: E402
from sqlalchemy.pool import StaticPool  # noqa: E402

engine = create_engine("sqlite://", connect_args={"check_same_thread": False}, poolclass=StaticPool)
SessionLocal = sessionmaker(bind=engine)

for name in ["jarvis_log_client", "paho", "paho.mqtt", "paho.mqtt.client", "utils"]:
    stub(name)
stub("db", SessionLocal=SessionLocal)
stub("repositories", NODE / "repositories")  # the real package, without its __init__
stub("services", NODE / "services")
stub("services.secret_service")

from models.command_data import CommandData  # noqa: E402

CommandData.metadata.create_all(engine, tables=[CommandData.__table__])

from jarvis_command_sdk import IJarvisCommand  # noqa: E402
from jarvis_command_sdk.storage import JarvisStorage, set_backend  # noqa: E402


def load_fixtures() -> dict:
    found = {}
    for path in sorted(FIXTURES.glob("*.py")):
        spec = importlib.util.spec_from_file_location(f"fixture_{path.stem}", path)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        for _, cls in inspect.getmembers(module, inspect.isclass):
            if issubclass(cls, IJarvisCommand) and cls.__module__ == module.__name__ and not inspect.isabstract(cls):
                cmd = cls()
                found[cmd.command_name] = cmd
    return found


COMMANDS = load_fixtures()


class Discovery:
    def get_command(self, name):
        return COMMANDS.get(name)

    def get_all_commands(self, include_disabled=False):
        return dict(COMMANDS)


stub("utils.command_discovery_service", get_command_discovery_service=lambda: Discovery())


def load(rel):
    name = rel.removesuffix(".py").replace("/", ".")
    spec = importlib.util.spec_from_file_location(name, NODE / rel)
    mod = importlib.util.module_from_spec(spec)
    sys.modules[name] = mod
    spec.loader.exec_module(mod)
    return mod


load("repositories/command_data_repository.py")
set_backend(load("services/storage_backend.py").NodeStorageBackend())
handler = load("services/command_data_handler.py")

PAST = datetime(2020, 1, 1, tzinfo=timezone.utc)
FUTURE = datetime(2099, 1, 1, tzinfo=timezone.utc)

# (storage name, key, record, expires_at). Insertion order and each record's key order matter:
# the Pi lists rows in insertion order and the SDK's default summary titles a row with its first
# string field.
SEED = [
    ("notes", "n3", {"title": "Groceries", "body": "milk", "done": False, "user_id": 7,
                     "items": [{"name": "egg", "qty": 12}], "big": 12345678901234567890}, None),
    ("notes", "n1", {"title": "Shared", "body": None, "done": True}, None),
    ("notes", "n2", {"title": "Private 8", "user_id": 8}, None),
    ("notes", "n4", {"title": "boom", "user_id": 7}, None),
    ("notes", "n5", {"title": "old", "user_id": 7}, PAST),
    ("notes", "n6", {"title": "later", "user_id": 7, "_meta": "x", "f": 1.5}, FUTURE),
    ("journal", "j000", {"zeta": "First string", "alpha": "Second", "n": 0}, None),
    *[("journal", f"j{i:03d}", {"n": i, "entry": f"entry {i}"}, None) for i in range(1, 201)],
    ("alarm_store", "a1", {"at": "2026-10-15T07:00:00"}, None),
    ("vault", "v1", {"secret": 1}, None),
]
for storage, key, record, expires in SEED:
    JarvisStorage(storage).save(key, record, expires)


class Client:
    def __init__(self):
        self.published = []

    def publish(self, topic, payload, qos=0):
        self.published.append((topic, payload, qos))


_cid = 0


def req(op, **payload):
    global _cid
    _cid += 1
    return op, {"correlation_id": f"c{_cid}", **payload}


U7 = {"requesting_user_id": 7}
STEPS = [
    req("commands"),
    req("schema", command_name="notes"),
    req("schema"),
    req("schema", command_name="vault"),
    req("schema", command_name="nope"),
    req("schema", command_name="journal"),
    req("list", command_name="notes", **U7),
    req("list", command_name="notes", requesting_user_id=8),
    req("list", command_name="notes", requesting_user_id=None),
    req("list", command_name="journal", **U7),
    req("list", command_name="alarm", **U7),
    req("list", command_name="vault", **U7),
    req("list", **U7),
    req("get", command_name="notes", key="n3", **U7),
    req("get", command_name="notes", key="n6", **U7),
    req("get", command_name="notes", key="n2", **U7),
    req("get", command_name="notes", key="n5", **U7),
    req("get", command_name="notes", **U7),
    req("get", command_name="alarm", key="a1", **U7),
    req("update", command_name="notes", key="n3", patch={
        "title": "Groceries!", "owner": 99, "data_key": "x", "scope": "household", "unknown": 1,
        "items": [{"name": "egg", "qty": 6}]}, **U7),
    req("update", command_name="notes", key="n3", patch={"owner": 1}, **U7),
    req("update", command_name="notes", key="n2", patch={"title": "mine"}, **U7),
    req("update", command_name="notes", key="n5", patch={"title": "revive"}, **U7),
    req("update", command_name="alarm", key="a1", patch={"at": "x"}, **U7),
    req("update", command_name="notes", key="n3", patch="nope", **U7),
    req("update", command_name="journal", key="j000", patch={"zeta": "x"}, **U7),
    req("get", command_name="notes", key="n3", **U7),
    req("create", command_name="notes", data={"title": "New", "scope": "household", "owner": 3,
                                              "user_id": 99, "id": "x", "data_key": "k"}, **U7),
    req("create", command_name="notes", data={"title": "Nobody"}, requesting_user_id=None),
    req("create", command_name="journal", data={"x": 1}, **U7),
    req("create", command_name="alarm", data={"at": "x"}, **U7),
    req("create", command_name="notes", data="nope", **U7),
    req("create", command_name="dose", data={"med": "ibuprofen"}, **U7),
    req("create", command_name="dose", data={"med": "ibuprofen", "at": "08:00", "extra": 1}, **U7),
    req("create", command_name="dose", data={"med": "crash", "at": "09:00"}, **U7),
    req("list", command_name="notes", **U7),
    req("list", command_name="dose", **U7),
    req("delete", command_name="notes", key="n2", **U7),
    req("delete", command_name="alarm", key="a1", **U7),
    req("delete", command_name="notes", key="zz", **U7),
    req("delete", command_name="notes", **U7),
    req("delete", command_name="notes", key="n3", **U7),
    req("delete", command_name="notes", key="<created:1>", **U7),
    req("list", command_name="notes", **U7),
    ("list", {"command_name": "notes", **U7}),  # no correlation id: no answer
]

created = []


def mask(text: str) -> str:
    for i, key in enumerate(created, 1):
        text = text.replace(key, f"<created:{i}>")
    return text


def unmask(value):
    text = json.dumps(value)
    for i, key in enumerate(created, 1):
        text = text.replace(f"<created:{i}>", key)
    return json.loads(text)


steps = []
for op, payload in STEPS:
    client = Client()
    topic = f"jarvis/nodes/{NODE_ID}/command-data/{op}"
    handler.handle_command_data_request(client, topic, json.dumps(unmask(payload)).encode(), op)
    assert len(client.published) <= 1, client.published
    response = None
    if client.published:
        rtopic, body, qos = client.published[0]
        assert rtopic == f"{topic}/response/{payload['correlation_id']}" and qos == 1, client.published
        response = json.loads(body)
        key = response.get("key") if isinstance(response, dict) else None
        if op == "create" and response.get("ok") and key and key not in created and "@" not in key:
            created.append(key)
        response = json.loads(mask(json.dumps(response)))
    steps.append({"op": op, "request": payload, "response": response})

out = {
    "node_id": NODE_ID,
    "seed": [{"storage": s, "key": k, "record": r, "expires_at": e.isoformat() if e else None} for s, k, r, e in SEED],
    "steps": steps,
}
json.dump(out, sys.stdout, indent=1, ensure_ascii=False)
print()
