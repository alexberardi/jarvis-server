"""Jarvis package sidecar.

Loads IJarvisCommand classes, from the agent's installed-packages file (a .json argument: see
load_packages) or, for the spike's --sidecar-commands, every module in a directory, and answers
JSON-lines requests on stdin:

    -> {"id": 1, "method": "describe"}
    <- {"id": 1, "result": {"tools": [{"name", "client_tool", "command", "package"}, ...],
                            "failed": {"package": "why its commands didn't load"}}}
    -> {"id": 2, "method": "call", "params": {"command_name", "arguments", "request": {...}}}
    <- {"id": 2, "result": {"output": {...}, "signals": {"wait_for_input", "clear_history",
                                                          "on_response_complete"?}}}
    -> {"id": 3, "method": "complete", "params": {"token": "1"}}
    <- {"id": 3, "result": {"ran": true}}
    -> {"id": 4, "method": "data_summaries", "params": {"command_name", "records": [{...}, ...]}}
    <- {"id": 4, "result": {"summaries": [{"title", "subtitle", "icon"} | null, ...]}}
    -> {"id": 5, "method": "data_create", "params": {"command_name", "fields", "requesting_user_id"}}
    <- {"id": 5, "result": {"key", "record"} | {"rejected": "..."} | {"failed": "..."}}
    <- {"id": N, "error": "..."}            on a protocol-level failure

A Pi's packages may also have device families and managers (the listing's "families" and
"managers"; describe adds them as "families"/"managers"). Their methods return the body the
Python node posts to jarvisd (docs/agent/go-pi-node.md §3.16):

    -> {"method": "device_scan"}                              <- {"devices": [...]}
    -> {"method": "device_list", "params": {"manager_name"}}  <- {"devices", "manager_name", "can_edit_devices"} | {"error"}
    -> {"method": "device_state", "params": {"details": {...}}}  <- {"entity_id", "domain", "state", "ui_hints"} | {..., "error"}
    -> {"method": "device_removed", "params": {"details": {...}}}  <- {"ran": bool}
    -> {"method": "store_auth", "params": {"provider", "values", "user_id"}}  <- {"stored": "family x" | null}
    -> {"method": "device_families"}                          <- {"families": [...], "managers": [...]}

Storage (JarvisStorage: command secrets and command data) is the agent's, not this process's
(DESIGN §16). A registered StorageBackend asks the Go agent over the same pipes, between a
request and its reply:

    <- {"from": "child", "id": K, "method": "storage.get_secret", "params": {...}}
    -> {"to": "child", "id": K, "result": ...}         or {"to": "child", "id": K, "error": "..."}

so a package reads the settings the household set from the mobile app exactly as on a Pi
(storage.get_secret(...)), with the requesting user set for user-scoped secrets.

With --backends (the Pi profile) the SDK's other node backends answer the same way: llm.*,
cc.*, timer.*, audio.*, alerts.*, inbox.* and signals.* go to the Go agent, which holds the
key, the timers, the alert queue and the speaker (docs/agent/go-pi-node.md §5.2).
CommandInvoker runs another loaded command in this process, and the SDK's stdlib logger
fallback writes to stderr. Without the flag (the desktop agent) none is registered, so a
command gets the SDK's safe defaults.

"signals" are the CommandResponse fields only the node acts on; they never reach
command-center. A command's on_response_complete callable (a media command's deferred "start
playing now") stays here under a token; the agent runs it with "complete" once the spoken reply
is over.

stdout carries protocol only: print() inside commands is redirected to stderr, which the Go
agent forwards to its log. The Go agent owns the node's identity, key and MQTT; this process
never sees them.
"""

import importlib
import importlib.machinery
import importlib.util
import inspect
import json
import pathlib
import re
import sys
import traceback

PROTO = sys.stdout
sys.stdout = sys.stderr  # a stray print() must not corrupt the protocol

from jarvis_command_sdk import IJarvisCommand, RequestInformation  # noqa: E402
from jarvis_command_sdk.context import set_current_user_id  # noqa: E402
from jarvis_command_sdk.storage import StorageBackend, set_backend  # noqa: E402

_next_ask = 0


def _ask(method: str, params: dict):
    """One reverse request to the Go agent; blocks for its answer (requests are serial)."""
    global _next_ask
    _next_ask += 1
    PROTO.write(json.dumps({"from": "child", "id": _next_ask, "method": method, "params": params}, default=str) + "\n")
    PROTO.flush()
    line = sys.stdin.readline()
    if not line:
        raise RuntimeError("agent closed the sidecar's input")
    msg = json.loads(line)
    if msg.get("to") != "child" or msg.get("id") != _next_ask:
        raise RuntimeError(f"unexpected answer to {method}: {line[:200]}")
    if msg.get("error"):
        raise RuntimeError(msg["error"])
    return msg.get("result")


class AgentStorage(StorageBackend):
    """JarvisStorage's backend: every call goes to the Go agent's settings store."""

    def save(self, command_name, data_key, data, expires_at=None):
        _ask("storage.save", {"command": command_name, "key": data_key, "data": data,
                              "expires_at": expires_at.isoformat() if expires_at else None})

    def get(self, command_name, data_key):
        return _ask("storage.get", {"command": command_name, "key": data_key})

    def get_all(self, command_name):
        return _ask("storage.get_all", {"command": command_name}) or []

    def delete(self, command_name, data_key):
        return bool(_ask("storage.delete", {"command": command_name, "key": data_key}))

    def delete_all(self, command_name):
        return int(_ask("storage.delete_all", {"command": command_name}) or 0)

    def get_secret(self, key, scope, user_id=None):
        return _ask("storage.get_secret", {"key": key, "scope": scope, "user_id": user_id})

    def set_secret(self, key, value, scope, value_type="string", user_id=None):
        _ask("storage.set_secret", {"key": key, "value": value, "scope": scope, "value_type": value_type,
                                    "user_id": user_id})

    def delete_secret(self, key, scope, user_id=None):
        _ask("storage.delete_secret", {"key": key, "scope": scope, "user_id": user_id})


# The loaded commands, by name: CommandInvoker's lookup.
COMMANDS: dict = {}


def register_backends() -> None:
    """The Pi's SDK backends. Imported here, not at the top: a desktop venv may hold an SDK
    older than these modules."""
    import dataclasses
    import logging
    from datetime import datetime

    from jarvis_command_sdk.agent import Alert
    from jarvis_command_sdk.alert_queue import AlertQueueBackend, set_alert_queue_backend
    from jarvis_command_sdk.audio_control import AudioControlBackend, set_audio_control_backend
    from jarvis_command_sdk.cc_http import CCHttpBackend, set_cc_backend
    from jarvis_command_sdk.command_invoker import CommandInvokerBackend, set_command_invoker_backend
    from jarvis_command_sdk.inbox import InboxBackend, set_inbox_backend
    from jarvis_command_sdk.llm import LLMBackend, set_llm_backend
    from jarvis_command_sdk.signals import SignalsBackend, set_signals_backend
    from jarvis_command_sdk.timer import TimerBackend, set_timer_backend

    # get_logger's stdlib fallback; stderr is the Go agent's log.
    logging.basicConfig(stream=sys.stderr, level=logging.INFO, format="%(name)s %(levelname)s: %(message)s")

    alert_fields = {f.name for f in dataclasses.fields(Alert)}

    def alert(d: dict):
        for k in ("created_at", "expires_at"):
            if isinstance(d.get(k), str):
                d[k] = datetime.fromisoformat(d[k])
        return Alert(**{k: v for k, v in d.items() if k in alert_fields})

    class AgentLLM(LLMBackend):
        def chat_text(self, prompt):
            return _ask("llm.chat_text", {"prompt": prompt})

    class AgentCC(CCHttpBackend):
        def post(self, path, json):
            return _ask("cc.post", {"path": path, "json": json})

    class AgentTimer(TimerBackend):
        def set(self, seconds, label=None):
            return _ask("timer.set", {"seconds": seconds, "label": label})

        def cancel(self, timer_id):
            return bool(_ask("timer.cancel", {"timer_id": timer_id}))

        def list_active(self):
            return _ask("timer.list_active", {}) or []

        def get(self, timer_id):
            return _ask("timer.get", {"timer_id": timer_id})

        def find_by_label(self, label):
            return _ask("timer.find_by_label", {"label": label})

        def clear_all(self):
            return int(_ask("timer.clear_all", {}) or 0)

    class AgentAudio(AudioControlBackend):
        def set_volume_percent(self, percent):
            return bool(_ask("audio.set_volume_percent", {"percent": percent}))

        def adjust_volume_percent(self, delta):
            return _ask("audio.adjust_volume_percent", {"delta": delta})

        def set_muted(self, muted):
            return bool(_ask("audio.set_muted", {"muted": muted}))

        def speak(self, text):
            return bool(_ask("audio.speak", {"text": text}))

    class AgentAlerts(AlertQueueBackend):
        def pending(self):
            return [alert(d) for d in _ask("alerts.pending", {}) or []]

        def flush(self):
            return [alert(d) for d in _ask("alerts.flush", {}) or []]

        def remove(self, ids):
            return int(_ask("alerts.remove", {"ids": list(ids)}) or 0)

        def count(self):
            return int(_ask("alerts.count", {}) or 0)

    class AgentInbox(InboxBackend):
        # No attachments parameter: a post with attachments gets "attachments_unsupported".
        def post_inbox_item(self, command_name, *, title, summary="", body="", category="general", metadata=None,
                            user_id=None, create_push_notification=False, target_type="household"):
            try:
                return _ask("inbox.post", {"command_name": command_name, "title": title, "summary": summary,
                                           "body": body, "category": category, "metadata": metadata,
                                           "user_id": user_id, "create_push_notification": create_push_notification,
                                           "target_type": target_type})
            except Exception as exc:  # a tag, never an exception, into a command
                print(f"sidecar: inbox post failed: {exc}", file=sys.stderr)
                return "http_error"

    class AgentSignals(SignalsBackend):
        def emit_signal(self, payload):
            try:
                return _ask("signals.emit", {"payload": payload})
            except Exception as exc:
                print(f"sidecar: signal emit failed: {exc}", file=sys.stderr)
                return "http_error"

    class InProcessInvoker(CommandInvokerBackend):
        """Another loaded command, in this process; it reads its own secrets through storage,
        as the requesting user of the call in progress."""

        def invoke(self, name, request_info, /, **args):
            cmd = COMMANDS.get(name)
            return None if cmd is None else cmd.execute(request_info, **args)

    set_llm_backend(AgentLLM())
    set_cc_backend(AgentCC())
    set_timer_backend(AgentTimer())
    set_audio_control_backend(AgentAudio())
    set_alert_queue_backend(AgentAlerts())
    set_inbox_backend(AgentInbox())
    set_signals_backend(AgentSignals())
    set_command_invoker_backend(InProcessInvoker())


def _commands_in(module) -> list:
    return [cls() for _, cls in inspect.getmembers(module, inspect.isclass)
            if issubclass(cls, IJarvisCommand) and cls.__module__ == module.__name__ and not inspect.isabstract(cls)]


def load_commands(directory: str) -> dict:
    """The spike's layout: every *.py in one directory is a module of commands."""
    found = {}
    for path in sorted(pathlib.Path(directory).glob("*.py")):
        spec = importlib.util.spec_from_file_location(f"jarvis_pkg_{path.stem}", path)
        if spec is None or spec.loader is None:
            continue
        module = importlib.util.module_from_spec(spec)
        try:
            spec.loader.exec_module(module)
        except Exception:
            print(f"sidecar: failed to load {path.name}:\n{traceback.format_exc()}", file=sys.stderr)
            continue
        for cmd in _commands_in(module):
            found[cmd.command_name] = cmd
    return found


# Installed packages whose commands didn't load, by package name: the agent refuses an install
# that lands here and reports the rest as unhealthy.
FAILED: dict = {}
# The package each command came from.
PACKAGE_OF: dict = {}
# The installed packages' device families by protocol_name, and device managers by name.
FAMILIES: dict = {}
MANAGERS: dict = {}


def load_packages(listing: str) -> dict:
    """Installed Pantry packages, from the agent's commands file:

        {"packages": [{"name": "pkg", "root": "/abs/dir", "commands": ["commands/x/command.py"]}],
         "builtins": {"package": "commands", "skip": ["routine_command"]}}   (builtins: a Pi's)

    The built-ins load first; a package command with a built-in's name is refused.

    Each package is imported as its own top-level package (jarvis_pkgs_<name>), so two packages
    may both have a commands/get_weather/ and a command's relative imports work. Its root also
    goes on sys.path for the shared code a bundle imports by absolute name, as on a Pi.
    """
    with open(listing, encoding="utf-8") as f:
        doc = json.load(f)
    packages = doc.get("packages") or []
    found = load_builtins(doc["builtins"]) if doc.get("builtins") else {}
    for pkg in packages:
        name, root = pkg["name"], pkg["root"]
        top = "jarvis_pkgs_" + re.sub(r"\W", "_", name)
        spec = importlib.machinery.ModuleSpec(top, None, is_package=True)
        spec.submodule_search_locations = [root]
        sys.modules[top] = importlib.util.module_from_spec(spec)
        if root not in sys.path:
            sys.path.append(root)
        for rel in pkg.get("commands") or []:
            dotted = top + "." + rel[: -len(".py")].replace("/", ".")
            try:
                module = importlib.import_module(dotted)
                commands = _commands_in(module)
            except BaseException as exc:  # a SystemExit at import time is a broken package too
                print(f"sidecar: {name}: failed to load {rel}:\n{traceback.format_exc()}", file=sys.stderr)
                FAILED[name] = f"{rel}: {type(exc).__name__}: {exc}"
                continue
            if not commands:
                FAILED[name] = f"{rel}: no IJarvisCommand in it"
                continue
            for cmd in commands:
                try:
                    cname = cmd.command_name
                except Exception as exc:
                    FAILED[name] = f"{rel}: command_name: {exc}"
                    continue
                if cname in found and PACKAGE_OF.get(cname) != name:
                    FAILED[name] = f"{rel}: command {cname} is already provided by {PACKAGE_OF.get(cname) or 'the node'}"
                    continue
                found[cname] = cmd
                PACKAGE_OF[cname] = name
        for rel in pkg.get("families") or []:
            _load_device_component(name, top, rel, "protocol_name", FAMILIES)
        for rel in pkg.get("managers") or []:
            _load_device_component(name, top, rel, "name", MANAGERS)
    return found


def _load_device_component(package: str, top: str, rel: str, key: str, into: dict) -> None:
    """A Pantry device family (device_families/<name>/protocol.py: an IJarvisDeviceProtocol)
    or device manager (device_managers/<name>/manager.py: an IJarvisDeviceManager)."""
    from jarvis_command_sdk import IJarvisDeviceManager, IJarvisDeviceProtocol

    base = IJarvisDeviceProtocol if into is FAMILIES else IJarvisDeviceManager
    try:
        module = importlib.import_module(top + "." + rel[: -len(".py")].replace("/", "."))
        found = [cls() for _, cls in inspect.getmembers(module, inspect.isclass)
                 if issubclass(cls, base) and cls.__module__ == module.__name__ and not inspect.isabstract(cls)]
        if not found:
            FAILED[package] = f"{rel}: no {base.__name__} in it"
        for obj in found:
            into[getattr(obj, key)] = obj
    except BaseException as exc:
        print(f"sidecar: {package}: failed to load {rel}:\n{traceback.format_exc()}", file=sys.stderr)
        FAILED[package] = f"{rel}: {type(exc).__name__}: {exc}"


def load_builtins(spec: dict) -> dict:
    """The node's built-in commands: every *_command module of an installed package (the
    commands package of jarvis-node-setup), less spec["skip"]. A module that fails to import is
    in FAILED as "builtin <module>"; one whose node-only code fails later fails that call."""
    import pkgutil

    found = {}
    skip = set(spec.get("skip") or [])
    try:
        pkg = importlib.import_module(spec["package"])
    except BaseException as exc:
        FAILED["builtin " + spec["package"]] = f"{type(exc).__name__}: {exc}"
        return found
    for info in sorted(pkgutil.iter_modules(pkg.__path__), key=lambda i: i.name):
        if info.ispkg or not info.name.endswith("_command") or info.name in skip:
            continue
        try:
            module = importlib.import_module(f"{spec['package']}.{info.name}")
            commands = _commands_in(module)
        except BaseException as exc:
            print(f"sidecar: failed to load built-in {info.name}:\n{traceback.format_exc()}", file=sys.stderr)
            FAILED["builtin " + info.name] = f"{type(exc).__name__}: {exc}"
            continue
        for cmd in commands:
            found[cmd.command_name] = cmd
    return found


def load(target: str) -> dict:
    return load_packages(target) if target.endswith(".json") else load_commands(target)


_SECRET_FIELDS = ("key", "scope", "description", "value_type", "required", "is_sensitive", "friendly_name",
                  "enum_values", "presets")


def _secrets(cmd) -> list:
    """The command's required_secrets for the settings snapshot; a bad one is skipped alone."""
    return _secrets_of(cmd, cmd.command_name)


def _browser(cmd):
    """The command's mobile data-browser settings (command_data_handler.py reads the same), or
    None when they can't be read: the command then stays out of the browser."""
    try:
        return {"mode": str(cmd.data_browser_mode), "storage_name": cmd.data_browser_storage_name,
                "supports_create": bool(cmd.data_browser_supports_create),
                "fields": [f.to_dict() for f in cmd.editable_fields()]}
    except Exception as exc:
        print(f"sidecar: {cmd.command_name}: data browser settings failed: {exc}", file=sys.stderr)
        return None


def _optional(cmd, attr):
    """A snapshot extra (associated_service, setup_guide) that a command may not have."""
    try:
        return getattr(cmd, attr, None) or None
    except Exception as exc:
        print(f"sidecar: {cmd.command_name}: {attr} failed: {exc}", file=sys.stderr)
        return None


def describe(commands: dict) -> dict:
    tools = []
    for name, cmd in commands.items():
        try:
            tools.append({"name": name, "client_tool": cmd.to_openai_tool_schema(), "command": cmd.get_command_schema(),
                          "secrets": _secrets(cmd), "data_browser": _browser(cmd),
                          "associated_service": _optional(cmd, "associated_service"),
                          "setup_guide": _optional(cmd, "setup_guide"), "package": PACKAGE_OF.get(name)})
        except Exception as exc:
            print(f"sidecar: skipping {name}: {exc}", file=sys.stderr)
            if name in PACKAGE_OF:
                FAILED[PACKAGE_OF[name]] = f"{name}: {type(exc).__name__}: {exc}"
    return {"tools": tools, "failed": FAILED, **device_families()}


def data_summaries(commands: dict, params: dict) -> dict:
    """display_summary for each record of a browser list; None where it raised (the agent then
    shows the Pi's fallback row)."""
    cmd = commands.get(params.get("command_name", ""))
    out = []
    for record in params.get("records") or []:
        try:
            out.append(cmd.display_summary(record).to_dict())
        except Exception as exc:
            print(f"sidecar: display_summary failed: {exc}", file=sys.stderr)
            out.append(None)
    return {"summaries": out}


def data_create(commands: dict, params: dict) -> dict:
    """data_browser_create; the command stamps the owner and persists through JarvisStorage."""
    cmd = commands.get(params.get("command_name", ""))
    if cmd is None:
        return {"rejected": "command not found"}
    try:
        key, record = cmd.data_browser_create(params.get("fields") or {}, params.get("requesting_user_id"))
    except ValueError as exc:
        return {"rejected": str(exc)}
    except Exception as exc:
        return {"failed": str(exc)}
    return {"key": key, "record": record}


# --- Devices: the Python node's device_scan/list/state handlers over the installed families ---
# (N: services/device_scan_handler.py, device_list_handler.py, device_state_handler.py,
# device_managers/jarvis_direct_manager.py, direct_device_service.remove_device). The Go agent
# owns MQTT and the uploads; these return what N: uploads.

SCAN_TIMEOUT = 10.0


def _run(coro):
    """One request's coroutine. asyncio.run shuts the loop's default executor down (N:'s note:
    a hand-rolled loop leaked to_thread workers per request)."""
    import asyncio

    return asyncio.run(coro)


def _configured(obj) -> bool:
    """validate_secrets() is empty (or absent): N:'s discovery skips a family or manager with
    missing secrets."""
    try:
        return not (obj.validate_secrets() if hasattr(obj, "validate_secrets") else [])
    except Exception as exc:
        print(f"sidecar: validate_secrets failed: {exc}", file=sys.stderr)
        return False


def _active_families() -> dict:
    return {name: fam for name, fam in FAMILIES.items() if _configured(fam)}


def _as_dict(dev) -> dict:
    import dataclasses

    d = dataclasses.asdict(dev) if dataclasses.is_dataclass(dev) else dict(vars(dev))
    d.pop("extra", None)  # internal data never leaves the node
    return d


async def _discover_all() -> list:
    """Every configured family's discover() at once, deduplicated by mac > ip > cloud_id >
    entity_id; a family that fails is logged and left out."""
    import asyncio

    families = _active_families()

    async def one(fam):
        return await fam.discover(timeout=SCAN_TIMEOUT)

    names = list(families)
    results = await asyncio.gather(*(one(families[n]) for n in names), return_exceptions=True)
    by_key = {}
    for name, result in zip(names, results):
        if isinstance(result, BaseException):
            print(f"sidecar: {name}: discover failed: {result}", file=sys.stderr)
            continue
        print(f"sidecar: {name}: discovered {len(result)} devices", file=sys.stderr)
        for dev in result:
            key = (dev.mac_address or "").lower() or dev.local_ip or dev.cloud_id or dev.entity_id
            by_key[key] = dev
    return list(by_key.values())


def device_scan(params: dict) -> dict:
    """{"devices": [...]} for POST /nodes/{id}/device-scan/{rid}/results: each device without
    its extra, with its protocol's supported_actions. A second device with an entity_id already
    taken gets "_<protocol>" appended (N:'s DeviceScannerService), so an import keeps both."""
    devices = _run(_discover_all())
    actions = {}
    for name, fam in FAMILIES.items():
        try:
            actions[name] = [a.to_dict() for a in fam.supported_actions]
        except Exception as exc:
            print(f"sidecar: {name}: supported_actions failed: {exc}", file=sys.stderr)
    seen = set()
    out = []
    for dev in devices:
        if dev.entity_id in seen:
            dev.entity_id = f"{dev.entity_id}_{dev.protocol}"
        seen.add(dev.entity_id)
        d = _as_dict(dev)
        if dev.protocol in actions:
            d["supported_actions"] = actions[dev.protocol]
        out.append(d)
    return {"devices": out}


class _JarvisDirect:
    """N:'s built-in JarvisDirectDeviceManager: the families' devices as manager devices."""

    name = "jarvis_direct"
    friendly_name = "Jarvis Direct"
    description = "WiFi devices controlled directly (LIFX, Kasa, Govee, etc.)"
    can_edit_devices = True
    required_secrets: list = []
    authentication = None

    async def collect_devices(self) -> list:
        from jarvis_command_sdk import DeviceManagerDevice

        return [DeviceManagerDevice(
            name=d.name, domain=d.domain, entity_id=d.entity_id, is_controllable=d.is_controllable,
            manufacturer=d.manufacturer, model=d.model, protocol=d.protocol, local_ip=d.local_ip,
            mac_address=d.mac_address, cloud_id=d.cloud_id, device_class=d.device_class, source="direct",
        ) for d in await _discover_all()]


def _managers(configured_only: bool) -> dict:
    out = {"jarvis_direct": _JarvisDirect()}
    for name, mgr in MANAGERS.items():
        if not configured_only or _configured(mgr):
            out[name] = mgr
    return out


def device_list(params: dict) -> dict:
    """The body for POST /nodes/{id}/device-list/{rid}/results: manager_name's devices, or every
    manager's for "all" (one that fails is skipped), or {"error": ...}."""
    name = params.get("manager_name") or "jarvis_direct"
    managers = _managers(configured_only=True)
    if name == "all":
        devices = []
        for mname, mgr in managers.items():
            try:
                devices.extend(_run(mgr.collect_devices()))
            except Exception as exc:
                print(f"sidecar: manager {mname} failed, skipping: {exc}", file=sys.stderr)
        return {"devices": [_as_dict(d) for d in devices], "manager_name": "all", "can_edit_devices": False}
    mgr = managers.get(name)
    if mgr is None:
        return {"error": f"Device manager '{name}' not found or not configured"}
    devices = _run(mgr.collect_devices())
    return {"devices": [_as_dict(d) for d in devices], "manager_name": mgr.name,
            "can_edit_devices": mgr.can_edit_devices}


def _temp_unit() -> str:
    """NEST_TEMP_UNIT (integration secret), F or C; F by default (N:'s climate handler)."""
    from jarvis_command_sdk.storage import get_backend

    try:
        backend = get_backend()
        unit = (backend.get_secret("NEST_TEMP_UNIT", "integration") if backend else None) or ""
    except Exception:
        unit = ""
    return unit.upper() if unit.upper() in ("F", "C") else "F"


def _num(v) -> int:
    return round(float(v))


def _normalize_light(raw: dict) -> dict:
    out = {"state": str(raw.get("state", "off")).lower()}
    if raw.get("brightness") is not None:
        b = float(raw["brightness"])
        if b > 100:  # HA's 0-255
            b = round(b / 255 * 100)
        out["brightness"] = round(b)
    rgb = raw.get("rgb") or raw.get("rgb_color")
    if isinstance(rgb, (list, tuple)) and len(rgb) == 3:
        out["rgb"] = [int(c) for c in rgb]
    if raw.get("hue") is not None:
        out["hue"] = _num(raw["hue"])
    if raw.get("saturation") is not None:
        out["saturation"] = _num(raw["saturation"])
    hs = raw.get("hs_color")
    if isinstance(hs, (list, tuple)) and len(hs) == 2:
        out.setdefault("hue", _num(hs[0]))
        out.setdefault("saturation", _num(hs[1]))
    ct = raw.get("color_temp") or raw.get("color_temp_kelvin")
    if ct is not None:
        out["color_temp"] = int(ct)
    return out


def _normalize_climate(raw: dict, unit: str) -> dict:
    out = {"unit": unit, "online": raw.get("online", True)}
    out["state"] = str(raw.get("hvac_action") or raw.get("state") or "off").lower()
    mode = raw.get("hvac_mode") or raw.get("mode") or "off"
    out["mode"] = str(mode).lower()
    current = raw.get("current_temperature")
    if current is None:
        current = raw.get("current_temperature_f" if unit == "F" else "current_temperature_c")
    if current is not None:
        out["current_temperature"] = _num(current)
    target = raw.get("target_temperature")
    if target is None:
        if str(mode).lower() == "cool" and "cool_setpoint" in raw:
            target = raw.get("cool_setpoint")
        elif "heat_setpoint" in raw:
            target = raw.get("heat_setpoint")
    if target is None:
        if "current_temperature" in raw or "current_temperature" in out:
            target = raw.get("temperature")
        elif raw.get("temperature") is not None and current is None:
            target = raw.get("temperature")
    if target is None:
        target = raw.get("target_temperature_f" if unit == "F" else "target_temperature_c")
    if target is not None:
        out["target_temperature"] = _num(target)
    if raw.get("humidity") is not None:
        out["humidity"] = _num(raw["humidity"])
    return out


def _normalize_kettle(raw: dict) -> dict:
    out = {"online": raw.get("online", True), "state": str(raw.get("state", "off")).lower()}
    for key in ("current_temperature", "target_temperature"):
        if raw.get(key) is not None:
            out[key] = _num(raw[key])
    unit = raw.get("unit", "C")
    out["unit"] = unit.upper() if isinstance(unit, str) else "C"
    mode = raw.get("mode") or raw.get("work_mode")
    if mode is not None:
        out["mode"] = str(mode).lower()
    return out


def _normalize_cover(raw: dict) -> dict:
    out = {"state": str(raw.get("state", "unknown")).lower()}
    if raw.get("current_position") is not None:
        out["position"] = _num(raw["current_position"])
    return out


def _normalize_media(raw: dict) -> dict:
    out = {"state": str(raw.get("state", "off")).lower()}
    if raw.get("volume_level") is not None:
        out["volume"] = round(float(raw["volume_level"]) * 100)
    for key in ("media_title", "media_artist", "source"):
        if raw.get(key):
            out[key] = raw[key]
    return out


def _normalize_camera(raw: dict) -> dict:
    online = raw.get("online", raw.get("state") != "offline")
    return {"state": "online" if online else "offline", "online": bool(online)}


def _normalize_lock(raw: dict) -> dict:
    state = str(raw.get("state", "unknown")).lower()
    return {"state": state, "is_locked": state == "locked"}


def _hints(control_type, features, default=(), lo=None, hi=None, step=None, unit=None) -> dict:
    return {"control_type": control_type, "features": features or list(default), "min_value": lo,
            "max_value": hi, "step": step, "unit": unit}


def _domain(domain: str, raw: dict, features):
    """N:'s device_families/domains: (normalized state, ui_hints) for domain, or None for a
    domain without a handler."""
    if domain == "light":
        return _normalize_light(raw), _hints("light", features, ("brightness", "color", "color_temp"), 0, 100, 1, "%")
    if domain == "climate":
        unit = _temp_unit()
        return _normalize_climate(raw, unit), _hints("thermostat", features, ("heat", "cool", "off"),
                                                     50 if unit == "F" else 10, 90 if unit == "F" else 32, 1, unit)
    if domain == "switch":
        return {"state": str(raw.get("state", "off")).lower()}, _hints("toggle", features)
    if domain == "lock":
        return _normalize_lock(raw), _hints("lock", features)
    if domain == "camera":
        return _normalize_camera(raw), _hints("camera", features)
    if domain == "cover":
        return _normalize_cover(raw), _hints("cover", features, (), 0, 100, 1, "%")
    if domain == "media_player":
        return _normalize_media(raw), _hints("media", features)
    if domain == "kettle":
        return _normalize_kettle(raw), _hints("kettle", features, ("boil", "keep_warm", "off"), 40, 100, 1, "C")
    return None


def device_state(params: dict) -> dict:
    """The body for POST /device-state-results/{rid}: the device's state from its family,
    normalized for its domain, with UI hints; {"needs_pairing": true} with control_type
    "pairing" when the family says so. A Home Assistant device (source not "direct") has no
    state here: N: asks HA through ha_shared, which this node doesn't have."""
    details = params.get("details") or {}
    entity_id = details.get("entity_id") or ""
    domain = details.get("domain") or ""
    if not entity_id:
        return {"error": "missing entity_id"}
    if not domain and "." in entity_id:
        domain = entity_id.split(".", 1)[0]
    raw = None
    if (details.get("source") or "direct") == "direct":
        fam = _active_families().get(details.get("protocol") or "")
        if fam is not None:
            try:
                raw = _run(fam.get_state(ip=details.get("local_ip") or "", entity_id=entity_id,
                                         cloud_id=details.get("cloud_id") or "",
                                         mac_address=details.get("mac_address") or ""))
            except Exception as exc:
                print(f"sidecar: device state query failed for {entity_id}: {exc}", file=sys.stderr)
    if raw is None:
        return {"entity_id": entity_id, "domain": domain, "error": "Failed to query device state"}
    if raw.get("needs_pairing"):
        return {"entity_id": entity_id, "domain": domain, "state": {"needs_pairing": True},
                "ui_hints": {"control_type": "pairing"}}
    available = [str(m).lower() for m in raw.get("available_features", raw.get("available_modes", []))]
    handled = _domain(domain, raw, available or None)
    state, hints = handled if handled else (raw, _hints("toggle", None))
    return {"entity_id": entity_id, "domain": domain, "state": state, "ui_hints": hints}


def device_removed(params: dict) -> dict:
    """The device's family's on_removed (e.g. a HomeKit unpair); best effort."""
    from jarvis_command_sdk import DiscoveredDevice

    details = params.get("details") or {}
    protocol = details.get("protocol") or ""
    fam = FAMILIES.get(protocol)
    if fam is None:
        return {"ran": False}
    entity_id = details.get("entity_id") or ""
    dev = DiscoveredDevice(name=details.get("name") or "", domain=details.get("domain") or "", manufacturer=protocol,
                           model=details.get("model") or "", protocol=protocol, entity_id=entity_id,
                           local_ip=details.get("local_ip"), mac_address=details.get("mac_address"),
                           cloud_id=details.get("cloud_id"))
    try:
        _run(fam.on_removed(dev, entity_id=entity_id))
    except Exception as exc:
        print(f"sidecar: {protocol}: on_removed failed for {entity_id}: {exc}", file=sys.stderr)
        return {"ran": False, "error": str(exc)}
    return {"ran": True}


def store_auth(commands: dict, params: dict) -> dict:
    """OAuth credentials jarvisd holds for provider, stored by the command or family whose
    authentication names it (N:'s _pull_auth_credentials), as the user who ran the flow."""
    provider = params.get("provider") or ""
    values = params.get("values") or {}
    owners = [("command", c.command_name, c) for c in commands.values()]
    owners += [("family", name, f) for name, f in FAMILIES.items()]
    for kind, name, owner in owners:
        try:
            auth = owner.authentication
        except Exception:
            continue
        if not auth or getattr(auth, "provider", None) != provider:
            continue
        set_current_user_id(params.get("user_id"))
        try:
            owner.store_auth_values(values)
        finally:
            set_current_user_id(None)
        return {"stored": f"{kind} {name}"}
    return {"stored": None}


def _guard(errors: list, what: str, fn, default):
    """N:'s _EntryGuard: a property that raises is recorded and replaced by default."""
    try:
        return fn()
    except Exception as exc:
        errors.append(f"{what}: {type(exc).__name__}: {exc}")
        return default


def device_families() -> dict:
    """The families and managers for the settings snapshot (N:'s entries; Go adds the secrets'
    values) and the heartbeat (a family's "active": its secrets are set)."""
    families = []
    for name, fam in FAMILIES.items():
        errs: list = []
        e = {"family_name": name,
             "friendly_name": _guard(errs, "friendly_name", lambda: fam.friendly_name, ""),
             "description": _guard(errs, "description", lambda: fam.description, ""),
             "connection_type": _guard(errs, "connection_type", lambda: fam.connection_type, ""),
             "supported_domains": _guard(errs, "supported_domains", lambda: list(fam.supported_domains), []),
             "supported_actions": _guard(errs, "supported_actions",
                                         lambda: [a.to_dict() for a in fam.supported_actions], []),
             "secrets": _secrets_of(fam, name), "is_configured": _configured(fam), "is_custom": True}
        auth = _guard(errs, "authentication", lambda: fam.authentication and fam.authentication.to_dict(), None)
        if auth:
            e["authentication"] = auth
        guide = _guard(errs, "setup_guide", lambda: getattr(fam, "setup_guide", None), None)
        if guide:
            e["setup_guide"] = guide
        if errs:
            e["_errors"] = errs
        families.append(e)
    managers = []
    for name, mgr in _managers(configured_only=False).items():
        errs = []
        avail = getattr(mgr, "is_available", None)  # N:'s own interface has it, the SDK's doesn't
        e = {"manager_name": name,
             "friendly_name": _guard(errs, "friendly_name", lambda: mgr.friendly_name, ""),
             "description": _guard(errs, "description", lambda: mgr.description, ""),
             "can_edit_devices": _guard(errs, "can_edit_devices", lambda: bool(mgr.can_edit_devices), False),
             "is_available": _guard(errs, "is_available", lambda: bool(avail()) if callable(avail) else True, False),
             "secrets": _secrets_of(mgr, name)}
        auth = _guard(errs, "authentication", lambda: mgr.authentication and mgr.authentication.to_dict(), None)
        if auth:
            e["authentication"] = auth
        if errs:
            e["_errors"] = errs
        managers.append(e)
    return {"families": families, "managers": managers}


def _secrets_of(obj, name: str) -> list:
    out = []
    try:
        declared = obj.required_secrets or []
    except Exception as exc:
        print(f"sidecar: {name}: required_secrets failed: {exc}", file=sys.stderr)
        return out
    for secret in declared:
        try:
            out.append({f: getattr(secret, f, None) for f in _SECRET_FIELDS})
        except Exception as exc:
            print(f"sidecar: {name}: skipping a secret: {exc}", file=sys.stderr)
    return out


# Deferred on_response_complete callables by token. Bounded: a token the agent never completes
# (a crashed turn) must not pin a command's state forever.
_PENDING: dict = {}
_PENDING_MAX = 16
_next_token = 0


def _stash(callback) -> str:
    global _next_token
    _next_token += 1
    token = str(_next_token)
    _PENDING[token] = callback
    while len(_PENDING) > _PENDING_MAX:
        _PENDING.pop(next(iter(_PENDING)))
    return token


def complete(params: dict) -> dict:
    callback = _PENDING.pop(str(params.get("token", "")), None)
    if callback is None:
        return {"ran": False}
    callback()
    return {"ran": True}


def call(commands: dict, params: dict) -> dict:
    name = params.get("command_name", "")
    cmd = commands.get(name)
    if cmd is None:
        return {"output": {"success": False, "error": f"Unknown command: {name}"}}
    req = params.get("request") or {}
    ri = RequestInformation(
        voice_command=req.get("voice_command") or "[desktop]",
        conversation_id=req.get("tool_call_id") or "desktop",
        user_id=req.get("user_id"),
    )
    # User-scoped secrets resolve to the requesting user, as on a Pi.
    set_current_user_id(req.get("user_id"))
    try:
        resp = cmd.execute(ri, **(params.get("arguments") or {}))
    except Exception as exc:
        return {"output": {"success": False, "error": str(exc)}}
    finally:
        set_current_user_id(None)
    output = dict(resp.context_data or {})
    if not resp.success:
        output["error"] = resp.error_details or "Command failed"
    output["success"] = resp.success
    signals = {"wait_for_input": bool(resp.wait_for_input), "clear_history": bool(resp.clear_history)}
    callback = getattr(resp, "on_response_complete", None)
    if callback is not None:
        signals["on_response_complete"] = _stash(callback)
    return {"output": output, "signals": signals}


DEVICE_METHODS = {"device_scan": device_scan, "device_list": device_list, "device_state": device_state,
                  "device_removed": device_removed, "device_families": lambda params: device_families()}


def main() -> None:
    set_backend(AgentStorage())
    if "--backends" in sys.argv[2:]:
        register_backends()
    commands = None
    while True:
        line = sys.stdin.readline()  # not `for line in sys.stdin`: _ask reads replies from it too
        if not line:
            break
        line = line.strip()
        if not line:
            continue
        if commands is None:
            # Loaded once the first request is read, so a command that touches storage while it
            # is constructed gets the agent's answer, not the next request, from stdin.
            commands = load(sys.argv[1])
            COMMANDS.update(commands)
            print(f"sidecar: loaded {sorted(commands)}", file=sys.stderr)
        msg_id = None
        try:
            msg = json.loads(line)
            msg_id = msg.get("id")
            method = msg.get("method")
            if method == "describe":
                reply = {"id": msg_id, "result": describe(commands)}
            elif method == "call":
                reply = {"id": msg_id, "result": call(commands, msg.get("params") or {})}
            elif method == "complete":
                reply = {"id": msg_id, "result": complete(msg.get("params") or {})}
            elif method == "data_summaries":
                reply = {"id": msg_id, "result": data_summaries(commands, msg.get("params") or {})}
            elif method == "data_create":
                reply = {"id": msg_id, "result": data_create(commands, msg.get("params") or {})}
            elif method in DEVICE_METHODS:
                reply = {"id": msg_id, "result": DEVICE_METHODS[method](msg.get("params") or {})}
            elif method == "store_auth":
                reply = {"id": msg_id, "result": store_auth(commands, msg.get("params") or {})}
            else:
                reply = {"id": msg_id, "error": f"unknown method {method!r}"}
        except Exception as exc:
            reply = {"id": msg_id, "error": f"{type(exc).__name__}: {exc}"}
        PROTO.write(json.dumps(reply, default=str) + "\n")
        PROTO.flush()


if __name__ == "__main__":
    main()
