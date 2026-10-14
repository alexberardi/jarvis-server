"""Export the secret-Sync goldens (go test reads internal/agent/nodesettings/testdata/
sync_golden.json).

The app's "Sync to other nodes" (NodeSettingsScreen handleSyncExecute) copies a service's
secrets from one node to others: it asks the SOURCE for a snapshot with include_values (and,
through command-center, the requesting user), takes each secret's value, and pushes them to
each TARGET as one encrypted settings:secrets config push, with __user_id__ when any of them
is user-scoped. A desktop agent can be either end, so it must build the source's secrets list
and apply the target's push exactly as a Pi does. This runs the REAL Python node functions:

  - services/settings_snapshot_service.py _build_secrets_list (the source side), and
  - services/config_push_service.py _dispatch_secrets (the target side),

with the secret store stubbed by a dict (it records every set_secret call). The crypto
around both is pinned separately by k2_golden.json (export_k2.py).

    uv run --with cryptography \\
        --with 'jarvis-command-sdk @ git+https://github.com/alexberardi/jarvis-command-sdk@main' \\
        python tools/golden/export_sync.py ../jarvis-node-setup \\
        > internal/agent/nodesettings/testdata/sync_golden.json
"""
import importlib.util
import json
import pathlib
import sys
import types

NODE = pathlib.Path(sys.argv[1]).resolve()


class _Any:
    def __getattr__(self, name):
        return _Any()

    def __call__(self, *a, **k):
        return _Any()


def stub(name, **attrs):
    m = types.ModuleType(name)
    m.__dict__["__getattr__"] = lambda attr: _Any()
    m.__path__ = []
    for k, v in attrs.items():
        setattr(m, k, v)
    sys.modules[name] = m
    return m


# The node's secret table: (key, scope, user_id) -> value.
STORE: dict = {}
CALLS: list = []


def get_secret_value(key, scope, user_id=None):
    return STORE.get((key, scope, user_id))


def get_secret_scope(key):
    # secret_service.get_secret_scope: a user-scoped row first (any user), then integration.
    if any(k == key and s == "user" for (k, s, _) in STORE):
        return "user"
    if (key, "integration", None) in STORE:
        return "integration"
    return None


def set_secret(key, value, scope, value_type="string", user_id=None):
    CALLS.append({"key": key, "value": value, "scope": scope, "user_id": user_id})
    STORE[(key, scope, user_id if scope == "user" else None)] = value


for name in [
    "jarvis_log_client", "clients", "clients.rest_client", "db", "repositories",
    "repositories.agent_registry_repository", "repositories.command_registry_repository",
    "repositories.disabled_fast_path_repository", "services", "utils",
    "utils.agent_discovery_service", "utils.audio_volume", "utils.command_discovery_service",
    "utils.config_service", "utils.device_family_discovery_service",
    "utils.device_manager_discovery_service", "utils.service_discovery", "utils.config_file",
    "utils.encryption_utils", "constants", "constants.config_policy",
]:
    stub(name)
stub("services.secret_service", get_secret_value=get_secret_value, get_secret_scope=get_secret_scope,
     set_secret=set_secret)

from jarvis_command_sdk import JarvisSecret  # noqa: E402


def load(rel):
    spec = importlib.util.spec_from_file_location(rel.replace("/", "_"), NODE / rel)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


snap_svc = load("services/settings_snapshot_service.py")
push_svc = load("services/config_push_service.py")

# --- source: a service's declared secrets and what the node holds ---

SPECS = [
    JarvisSecret("SVC_API_KEY", "The service's API key", "integration", "string"),
    JarvisSecret("SVC_URL", "Where it lives", "integration", "string", is_sensitive=False, friendly_name="URL"),
    JarvisSecret("SVC_UNITS", "Units", "integration", "string", required=False, is_sensitive=False,
                 enum_values=["metric", "imperial"], presets={"metric": {"SVC_URL": "http://metric"}}),
    JarvisSecret("SVC_TOKEN", "Your personal token", "user", "string"),
    JarvisSecret("SVC_NICK", "What to call you", "user", "string", required=False, is_sensitive=False),
    JarvisSecret("SVC_UNSET", "Never set", "integration", "string"),
]
SOURCE = {
    ("SVC_API_KEY", "integration", None): "k-123",
    ("SVC_URL", "integration", None): "http://svc.local:8123",
    ("SVC_TOKEN", "user", 7): "tok-7",
    ("SVC_TOKEN", "user", 8): "tok-8",
    ("SVC_NICK", "user", 7): "Ace",
}
STORE.update(SOURCE)

sources = []
for include_values in (False, True):
    for user_id in (None, 7, 8):
        errors: list = []
        got = snap_svc._build_secrets_list(SPECS, include_values=include_values, user_id=user_id,
                                           log_ctx={}, errors=errors)
        assert not errors, errors
        sources.append({"include_values": include_values, "user_id": user_id, "secrets": got})

# --- target: pushes the app sends, applied to a node that already holds some secrets ---

TARGET = {
    ("SVC_API_KEY", "integration", None): "old",
}
pushes = [
    # A service of integration secrets: each key keeps its stored scope, else integration.
    {"SVC_API_KEY": "k-123", "SVC_URL": "http://svc.local:8123"},
    # Any user-scoped secret selected: the app adds __user_id__ and every value is that user's.
    {"SVC_API_KEY": "k-123", "SVC_TOKEN": "tok-7", "__user_id__": "7"},
    # Values the app's JSON can carry; "" is how the app clears a value.
    {"SVC_PORT": 8123, "SVC_ON": True, "SVC_URL": ""},
    {"SVC_NICK": "Ace", "__user_id__": ""},
]
targets = []
for push in pushes:
    STORE.clear()
    STORE.update(TARGET)
    CALLS.clear()
    push_svc._dispatch_secrets(dict(push))
    targets.append({"before": [{"key": k, "scope": s, "user_id": u, "value": v} for (k, s, u), v in TARGET.items()],
                    "push": push, "set_secret_calls": list(CALLS)})

out = {
    "specs": [{f: getattr(s, f) for f in ("key", "scope", "description", "value_type", "required",
                                           "is_sensitive", "friendly_name", "enum_values", "presets")}
              for s in SPECS],
    "source_store": [{"key": k, "scope": s, "user_id": u, "value": v} for (k, s, u), v in SOURCE.items()],
    "sources": sources,
    "targets": targets,
}
json.dump(out, sys.stdout, indent=1, ensure_ascii=False)
print()
