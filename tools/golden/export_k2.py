"""Export the K2 goldens for internal/agent/nodesettings (go test reads testdata/k2_golden.json).

The desktop agent must be byte-compatible with the Python node and the mobile app (DESIGN §16).
This loads the REAL Python node functions, with their imports stubbed:

  - services/settings_snapshot_service.py encrypt_snapshot (node → mobile), and
  - services/config_push_service.py _decrypt_config (mobile → node),

then fixes K2 and the nonce, so Go can be checked against exact bytes.

    uv run --with cryptography python tools/golden/export_k2.py ../jarvis-node-setup \
        > internal/agent/nodesettings/testdata/k2_golden.json

Config pushes are encrypted the way the mobile app's native bridge does it
(jarvis-node-mobile src/services/configPushService.ts: AES-256-GCM, AAD "{node}:{config_type}",
the bridge base64url-decodes its plaintext argument, so the GCM plaintext is the raw JSON), and
each one is checked by decrypting it with the node's own _decrypt_config before it is written.
"""
import base64
import importlib.util
import json
import os
import pathlib
import sys
import types
from datetime import datetime

from cryptography.hazmat.primitives.ciphers.aead import AESGCM

NODE = pathlib.Path(sys.argv[1])
K2 = bytes(range(32))  # a fixed test key, never a real one
NODE_ID = "golden-node-1"


class _Any:
    def __getattr__(self, name):
        return _Any()

    def __call__(self, *a, **k):
        return _Any()


def stub(name, **attrs):
    m = types.ModuleType(name)
    m.__dict__["__getattr__"] = lambda attr: _Any()
    m.__path__ = []  # importable as a package
    for k, v in attrs.items():
        setattr(m, k, v)
    sys.modules[name] = m
    return m


class K2Data:
    def __init__(self):
        self.k2, self.kid, self.created_at = K2, "golden-kid", datetime(2026, 10, 14)


for name in [
    "jarvis_log_client", "clients", "clients.rest_client", "db", "repositories",
    "repositories.agent_registry_repository", "repositories.command_registry_repository",
    "repositories.disabled_fast_path_repository", "services", "services.secret_service", "utils",
    "utils.agent_discovery_service", "utils.audio_volume", "utils.command_discovery_service",
    "utils.config_service", "utils.device_family_discovery_service",
    "utils.device_manager_discovery_service", "utils.service_discovery", "utils.config_file",
    "constants", "constants.config_policy",
]:
    stub(name)
stub("utils.encryption_utils", get_k2=lambda: K2Data())


def load(rel):
    spec = importlib.util.spec_from_file_location(rel.replace("/", "_"), NODE / rel)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


snap_svc = load("services/settings_snapshot_service.py")
push_svc = load("services/config_push_service.py")


def b64(b):
    return base64.urlsafe_b64encode(b).rstrip(b"=").decode()


out = {"k2": b64(K2), "node_id": NODE_ID, "snapshots": [], "pushes": []}

snapshots = [
    {"schema_version": 1, "commands_schema_version": 2, "commands": [], "agents": [],
     "device_families": [], "device_managers": [], "node_config": {"hardware": {"hat_detected": False}}},
    {"schema_version": 1, "commands_schema_version": 2, "commands": [
        {"command_name": "get_weather", "description": "Weather – «ünïcode» 天気", "enabled": True,
         "secrets": [{"key": "WEATHER_API_KEY", "scope": "integration", "description": "API key",
                      "value_type": "string", "required": True, "is_sensitive": True, "is_set": True}]}]},
]
for i, snap in enumerate(snapshots):
    nonce = bytes([i + 1]) * 12
    real = os.urandom
    os.urandom = lambda n, nonce=nonce: nonce[:n]
    try:
        enc = snap_svc.encrypt_snapshot(snap, NODE_ID)
    finally:
        os.urandom = real
    # The exact plaintext the node encrypted (json.dumps with compact separators).
    out["snapshots"].append({"plaintext": json.dumps(snap, separators=(",", ":")), **enc})

pushes = [
    ("settings:secrets", {"WEATHER_API_KEY": "abc123", "UNITS": "metric"}),
    ("settings:secrets", {"EMAIL_PASSWORD": "pä55 wörd", "__user_id__": "7"}),
    ("settings:secrets", {"WEATHER_API_KEY": ""}),
    ("command_registry", {"command_name": "get_weather", "enabled": "false"}),
    ("node_config", {"allow_updates": True, "__issued_at": 1760400000}),
]
for i, (ctype, data) in enumerate(pushes):
    nonce = bytes([0x40 + i]) * 12
    plaintext = json.dumps(data).encode()  # JSON.stringify, then the bridge's raw bytes
    ct = AESGCM(K2).encrypt(nonce, plaintext, f"{NODE_ID}:{ctype}".encode())
    item = {"config_type": ctype, "ciphertext": b64(ct[:-16]), "nonce": b64(nonce), "tag": b64(ct[-16:])}
    got = push_svc._decrypt_config(item["ciphertext"], item["nonce"], item["tag"], NODE_ID, ctype)
    assert got == data, (got, data)
    item["json"] = data
    out["pushes"].append(item)

json.dump(out, sys.stdout, indent=2, ensure_ascii=False)
print()
