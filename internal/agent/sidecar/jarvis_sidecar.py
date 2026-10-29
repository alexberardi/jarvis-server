"""Jarvis package sidecar.

Loads IJarvisCommand classes, from the agent's installed-packages file (a .json argument: see
load_packages) or, for the spike's --sidecar-commands, every module in a directory, and answers
JSON-lines requests on stdin:

    -> {"id": 1, "method": "describe"}
    <- {"id": 1, "result": {"tools": [{"name", "client_tool", "command", "package",
                                       "takes_over_playback_binary"}, ...],
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
    -> {"id": 6, "method": "reminders_due"}
    <- {"id": 6, "result": {"due": [{"id", "text", "user_id"}, ...], "pending": N}}
    -> {"id": 7, "method": "agent_run", "params": {"name": "comfyui_jobs"}}
    <- {"id": 7, "result": {"alerts": [{Alert.to_dict()}, ...]} | {"error": "..."}}
    <- {"id": N, "error": "..."}            on a protocol-level failure

A Pi's packages may also have background agents (the listing's "agents": IJarvisAgent
modules; describe adds them as "agents": [{"name", "package", "interval_seconds",
"run_on_startup"}]). The Go agent owns their schedule (internal/agent/pkgagents) and runs each
with agent_run; what they post (inbox items, pushes) goes through the backends below.

A Pi's packages may also have device families and managers (the listing's "families" and
"managers"; describe adds them as "families"/"managers"). Their methods return the body the
Python node posts to jarvisd (docs/agent/go-pi-node.md §3.17):

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

import ast
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


def _log_client_shim():
    """jarvis_log_client, the Python node's own log package, which N:services/reminder_service
    imports: JarvisLogger(service=...).info(msg, **fields) and the like, to stdlib logging
    (stderr, the Go agent's log)."""
    import logging
    import types

    class JarvisLogger:
        def __init__(self, service="jarvis-node", **_):
            self._log = logging.getLogger(service)

        def _emit(self, level, msg, fields):
            if fields:
                msg = f"{msg} " + " ".join(f"{k}={v}" for k, v in fields.items())
            self._log.log(level, msg)

        def debug(self, msg, **fields):
            self._emit(logging.DEBUG, msg, fields)

        def info(self, msg, **fields):
            self._emit(logging.INFO, msg, fields)

        def warning(self, msg, **fields):
            self._emit(logging.WARNING, msg, fields)

        warn = warning

        def error(self, msg, **fields):
            self._emit(logging.ERROR, msg, fields)

        exception = error

        def critical(self, msg, **fields):
            self._emit(logging.CRITICAL, msg, fields)

    mod = types.ModuleType("jarvis_log_client")
    mod.JarvisLogger = JarvisLogger
    return mod


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
    if importlib.util.find_spec("jarvis_log_client") is None:
        sys.modules["jarvis_log_client"] = _log_client_shim()

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
        # Attachments (already validated by JarvisInbox.post) go to the agent as files, which it
        # uploads (N:services/inbox_backend.py): a path as it is, data through a temp file
        # removed once the post returns, so a 25 MB video never crosses the pipe.
        def post_inbox_item(self, command_name, *, title, summary="", body="", category="general", metadata=None,
                            user_id=None, create_push_notification=False, target_type="household", attachments=None):
            import os
            import tempfile

            temps = []
            try:
                files = []
                for att in attachments or []:
                    path = att.path
                    if path is None:
                        fd, path = tempfile.mkstemp(prefix="jarvis-inbox-")
                        temps.append(path)
                        with os.fdopen(fd, "wb") as f:
                            f.write(att.data)
                    files.append({"path": os.path.abspath(os.fspath(path)), "mime": att.mime, "filename": att.filename})
                params = {"command_name": command_name, "title": title, "summary": summary, "body": body,
                          "category": category, "metadata": metadata, "user_id": user_id,
                          "create_push_notification": create_push_notification, "target_type": target_type}
                if files:
                    params["attachments"] = files
                return _ask("inbox.post", params)
            except Exception as exc:  # a tag, never an exception, into a command
                print(f"sidecar: inbox post failed: {exc}", file=sys.stderr)
                return "http_error"
            finally:
                for path in temps:
                    try:
                        os.remove(path)
                    except OSError:
                        pass

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
# The installed packages' background agents by name, and the package each came from.
AGENTS: dict = {}
AGENT_PACKAGE: dict = {}


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
    _share_top_level([pkg["root"] for pkg in packages])
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
        for rel in pkg.get("agents") or []:
            _load_agent(name, top, rel)
    return found


def _share_top_level(roots: list) -> None:
    """A top-level package that several package roots have (each its own device_families/) is
    one package over all their directories, as the Python node's single device_families tree
    was: otherwise the first root on sys.path takes the name and the others' absolute imports
    (from device_families.zwave import …) fail. Only when every __init__.py is empty or a
    docstring and comments: one with code is left to the normal import (first root wins)."""
    dirs = {}
    for root in roots:
        try:
            entries = sorted(pathlib.Path(root).iterdir())
        except OSError:
            continue
        for d in entries:
            if d.name.isidentifier() and (d / "__init__.py").is_file():
                dirs.setdefault(d.name, []).append(d)
    for name, found in dirs.items():
        if len(found) < 2 or name in sys.modules or not all(_empty_init(d / "__init__.py") for d in found):
            continue
        spec = importlib.machinery.ModuleSpec(name, None, is_package=True)
        spec.submodule_search_locations = [str(d) for d in found]
        sys.modules[name] = importlib.util.module_from_spec(spec)


def _empty_init(path: pathlib.Path) -> bool:
    try:
        body = ast.parse(path.read_text(encoding="utf-8")).body
    except (OSError, SyntaxError, ValueError):
        return False
    return all(isinstance(n, ast.Expr) and isinstance(n.value, ast.Constant) for n in body)


def _load_agent(package: str, top: str, rel: str) -> None:
    """A Pantry background agent (agents/<name>/agent.py: an IJarvisAgent, as
    N:utils/agent_discovery_service finds it). One that doesn't load fails its package."""
    from jarvis_command_sdk import IJarvisAgent

    try:
        module = importlib.import_module(top + "." + rel[: -len(".py")].replace("/", "."))
        found = [cls() for _, cls in inspect.getmembers(module, inspect.isclass)
                 if issubclass(cls, IJarvisAgent) and cls.__module__ == module.__name__ and not inspect.isabstract(cls)]
        if not found:
            FAILED[package] = f"{rel}: no IJarvisAgent in it"
        for agent in found:
            name = agent.name
            if name in AGENTS and AGENT_PACKAGE.get(name) != package:
                FAILED[package] = f"{rel}: agent {name} is already provided by {AGENT_PACKAGE[name]}"
                continue
            AGENTS[name] = agent
            AGENT_PACKAGE[name] = package
    except BaseException as exc:
        print(f"sidecar: {package}: failed to load {rel}:\n{traceback.format_exc()}", file=sys.stderr)
        FAILED[package] = f"{rel}: {type(exc).__name__}: {exc}"


def describe_agents() -> list:
    """The loaded agents and their schedules; one whose schedule can't be read fails its package."""
    out = []
    for name in sorted(AGENTS):
        try:
            sched = AGENTS[name].schedule
            out.append({"name": name, "package": AGENT_PACKAGE[name], "interval_seconds": int(sched.interval_seconds),
                        "run_on_startup": bool(sched.run_on_startup)})
        except Exception as exc:
            print(f"sidecar: skipping agent {name}: {exc}", file=sys.stderr)
            FAILED[AGENT_PACKAGE[name]] = f"agent {name}: schedule: {type(exc).__name__}: {exc}"
    return out


def agent_run(params: dict) -> dict:
    """One run of an agent (N:services/agent_scheduler_service._run_agent_safe; the Go agent
    owns the schedule and the alert queue): await run(), then its alerts. A run that raises is
    {"error"}; alerts that can't be read are none."""
    name = params.get("name")
    agent = AGENTS.get(name)
    if agent is None:
        raise RuntimeError(f"no agent {name!r} in this sidecar")
    set_current_user_id(None)
    try:
        _run(agent.run())
    except BaseException as exc:  # a SystemExit in run() must not end the sidecar
        print(f"sidecar: agent {name} failed:\n{traceback.format_exc()}", file=sys.stderr)
        return {"error": f"{type(exc).__name__}: {exc}"}
    alerts = []
    try:
        alerts = [a.to_dict() for a in agent.get_alerts() or []]
    except Exception as exc:
        print(f"sidecar: agent {name}: alerts not read: {exc}", file=sys.stderr)
    return {"alerts": alerts}


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
    if "reminder" in found:
        # The Python node restores its reminders at boot (N:scripts/main.py); each sidecar does
        # here, before the command or reminders_due reads the service's cache.
        try:
            importlib.import_module("services.reminder_service").initialize_reminder_service()
        except BaseException as exc:
            print(f"sidecar: reminders not restored:\n{traceback.format_exc()}", file=sys.stderr)
            FAILED["builtin reminder_command"] = f"{type(exc).__name__}: {exc}"
            del found["reminder"]
    return found


def reminders_due() -> dict:
    """One run of N:agents/reminder_agent.py (the Go agent owns its schedule and the alerts): drop
    the one-shot reminders announced past the snooze window, mark the due ones announced (a
    recurring one moves to its next time) and push each to its owner's phone when the reminder
    settings' push_notifications is on. "pending" is how many were not announced before."""
    from jarvis_command_sdk import UserSettings
    from jarvis_command_sdk.cc_http import cc_post
    from services.reminder_service import get_reminder_service

    service = get_reminder_service()
    service.cleanup_expired()
    pending = len(service.get_all_reminders(include_announced=False))
    if not pending:
        return {"due": [], "pending": 0}
    push = UserSettings("reminder").is_enabled("push_notifications")
    due = []
    for r in service.get_due_reminders():
        service.mark_announced(r.reminder_id)
        if push:
            payload = {"title": "Reminder", "body": r.text, "priority": "high", "category": "reminder"}
            if r.user_id is not None:
                payload["user_id"] = r.user_id
                payload["target_type"] = "user"
            if cc_post("/api/v0/node/push-notification", payload) is None:
                print(f"sidecar: reminder push failed: {r.reminder_id}", file=sys.stderr)
        due.append({"id": r.reminder_id, "text": r.text, "user_id": r.user_id})
    return {"due": due, "pending": pending}


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


def _context_ops(cmd) -> list:
    """The command's declared context operations (N:services/context_handler.py _declared_ops):
    a bad implementation declares none rather than breaking describe."""
    try:
        ops = list(getattr(cmd, "context_operations", None) or [])
        return [op.to_dict() if hasattr(op, "to_dict") else {"name": str(op)} for op in ops]
    except Exception as exc:
        print(f"sidecar: {cmd.command_name}: context_operations failed: {exc}", file=sys.stderr)
        return []


def _takeover(cmd):
    """The player binary a music command controls (takes_over_playback_binary), else None: the
    agent stops the other players before its "play" runs."""
    binary = _optional(cmd, "takes_over_playback_binary")
    return binary if isinstance(binary, str) else None


def describe(commands: dict) -> dict:
    tools = []
    for name, cmd in commands.items():
        try:
            tools.append({"name": name, "client_tool": cmd.to_openai_tool_schema(), "command": cmd.get_command_schema(),
                          "secrets": _secrets(cmd), "data_browser": _browser(cmd),
                          "associated_service": _optional(cmd, "associated_service"),
                          "setup_guide": _optional(cmd, "setup_guide"), "package": PACKAGE_OF.get(name),
                          "context_operations": _context_ops(cmd),
                          "takes_over_playback_binary": _takeover(cmd)})
        except Exception as exc:
            print(f"sidecar: skipping {name}: {exc}", file=sys.stderr)
            if name in PACKAGE_OF:
                FAILED[PACKAGE_OF[name]] = f"{name}: {type(exc).__name__}: {exc}"
    agents = describe_agents()
    return {"tools": tools, "failed": FAILED, "agents": agents, **device_families()}


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
    out = {"state": str(raw.get("state", "unknown")).lower()}
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
    # An unreadable state is "unknown"; the mode stays a real enum ("off" is a valid hvac mode).
    out["state"] = str(raw.get("hvac_action") or raw.get("state") or "unknown").lower()
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
    out = {"online": raw.get("online", True), "state": str(raw.get("state", "unknown")).lower()}
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
    out = {"state": str(raw.get("state", "unknown")).lower()}
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
        return {"state": str(raw.get("state", "unknown")).lower()}, _hints("toggle", features)
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


def _get_state(fam, details: dict):
    """A family's get_state, called the way it is written. The SDK's signature is
    get_state(ip, **kwargs) and gets the device's fields as keywords; families written from
    jarvis-node-setup's documented example take get_state(device) (Apple, LIFX, Kasa, Schlage,
    SimpliSafe, Home Connect) and get the DiscoveredDevice, as control() does."""
    import inspect

    from jarvis_command_sdk import DiscoveredDevice

    entity_id = details.get("entity_id") or ""
    try:
        params = inspect.signature(fam.get_state).parameters
    except (TypeError, ValueError):
        params = {"ip": None}
    if "ip" not in params:
        protocol = details.get("protocol") or ""
        return fam.get_state(DiscoveredDevice(
            entity_id=entity_id, name=details.get("name") or entity_id,
            domain=details.get("domain") or "switch", manufacturer=protocol, model=details.get("model") or "",
            protocol=protocol, cloud_id=details.get("cloud_id"), local_ip=details.get("local_ip"),
            mac_address=details.get("mac_address")))
    return fam.get_state(ip=details.get("local_ip") or "", entity_id=entity_id,
                         cloud_id=details.get("cloud_id") or "",
                         mac_address=details.get("mac_address") or "",
                         model=details.get("model") or "")


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
                raw = _run(_get_state(fam, details))
            except Exception as exc:
                print(f"sidecar: device state query failed for {entity_id}: {exc}", file=sys.stderr)
    if raw is None and details.get("source") == "home_assistant":
        try:
            from ha_shared.home_assistant_service import HomeAssistantService
            result = _run(HomeAssistantService().get_state(entity_id))
            if result.success:
                raw = {**result.attributes, "state": result.state}
        except Exception:
            pass
    if raw is None:
        return {"entity_id": entity_id, "domain": domain, "state": {"state": "unknown"},
                "error": "Failed to query device state"}
    if raw.get("needs_pairing"):
        return {"entity_id": entity_id, "domain": domain, "state": {"needs_pairing": True},
                "ui_hints": {"control_type": "pairing"}}
    if raw.get("error"):
        # The family answered but couldn't read the device: "unknown", never "off".
        return {"entity_id": entity_id, "domain": domain, "state": {"state": "unknown"},
                "error": str(raw["error"])}
    available = [str(m).lower() for m in raw.get("available_features", raw.get("available_modes", []))]
    handled = _domain(domain, raw, available or None)
    state, hints = handled if handled else (raw, _hints("toggle", None))
    return {"entity_id": entity_id, "domain": domain, "state": state, "ui_hints": hints}


def InvokeDisplayMedia(params: dict) -> dict:
    """Use the installed Home Assistant integration and its existing node credentials."""
    entity = params.get("entity_id", "")
    action = params.get("action", "")
    if not entity.startswith("media_player."):
        return {"success": False, "error": "Invalid media destination"}
    services = {"play": "play_media", "pause": "media_pause", "resume": "media_play",
                "stop": "media_stop", "seek": "media_seek"}
    try:
        from ha_shared.home_assistant_service import HomeAssistantService
        ha = HomeAssistantService()
        state = _run(ha.get_state(entity))
        if not state.success or state.state in ("unavailable", "unknown"):
            return {"success": False, "error": "Media destination unavailable"}
        if action == "status":
            return {"success": True, "state": state.state, "position": state.attributes.get("media_position")}
        service = services.get(action)
        if not service:
            return {"success": False, "error": "Unsupported media action"}
        features = int(state.attributes.get("supported_features", 0))
        required = {"play": 512, "pause": 1, "resume": 16384, "stop": 4096, "seek": 2}[action]
        if not features & required:
            return {"success": False, "error": "Destination does not advertise this media action"}
        data = {}
        if action == "play":
            from urllib.parse import urlparse
            target = params.get("url", "")
            if urlparse(target).scheme not in ("http", "https"):
                return {"success": False, "error": "Use a direct HTTP(S) media link"}
            data = {"media_content_id": target, "media_content_type": "video"}
        elif action == "seek":
            seconds = float(params.get("seconds", 0))
            if not 0 <= seconds <= 864000:
                return {"success": False, "error": "Invalid seek position"}
            data = {"seek_position": seconds}
        result = _run(ha.call_service("media_player", service, entity, data))
        return {"success": bool(result.success), "error": None if result.success else result.error}
    except Exception:
        return {"success": False, "error": "Home Assistant media integration unavailable"}


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


def context_query(commands: dict, params: dict) -> dict:
    """Run one declared context operation (the agent has checked it is declared and its required
    params are there) as the requesting user, as on a voice turn. A raise comes back as
    {"raised": message}; a result is its to_dict(), or {ok, data, error} for a bare value."""
    cmd = commands.get(params.get("command_name", ""))
    if cmd is None:
        return {"raised": "command not loaded"}
    set_current_user_id(params.get("user_id"))
    try:
        result = cmd.execute_context_operation(params.get("operation", ""), params.get("params") or {})
    except Exception as exc:
        return {"raised": str(exc)}
    finally:
        set_current_user_id(None)
    if hasattr(result, "to_dict"):
        return {"result": result.to_dict()}
    return {"result": {"ok": True, "data": result, "error": None}}


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
    arguments = dict(params.get("arguments") or {})
    # The model's arguments fixed up first, as N:'s command execution does for every tool call
    # (a string where the command takes a list, "true" for a boolean, a missing query).
    try:
        arguments = cmd.post_process_tool_call(arguments, req.get("voice_command") or "") or arguments
    except Exception as exc:
        print(f"sidecar: {name}: post_process_tool_call failed: {exc}", file=sys.stderr)
    # User-scoped secrets resolve to the requesting user, as on a Pi.
    set_current_user_id(req.get("user_id"))
    try:
        resp = cmd.execute(ri, **arguments)
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


# --- Actions and callbacks: N:'s handle_action and handle_callback past the MQTT and HTTP, which
# the Go agent does (internal/agent/actions); these return the body N: posts. ---

_CONTROL_LOOP = None


def _control_loop():
    """N:'s persistent device loop (_get_device_protocol_loop): a family's pairing session
    (pair_start, then pair_finish with the PIN) lives on the loop it started on, across calls."""
    import asyncio

    global _CONTROL_LOOP
    if _CONTROL_LOOP is None or _CONTROL_LOOP.is_closed():
        _CONTROL_LOOP = asyncio.new_event_loop()
    return _CONTROL_LOOP


def _device_control(action_name: str, context: dict) -> dict:
    """N:'s _handle_device_protocol_control: control_device with no such command goes to the
    device's family, configured or not. As on N:, the family's control() gets the device itself
    where the SDK says ip, and the whole action context as data."""
    from jarvis_command_sdk import DiscoveredDevice

    protocol = context.get("protocol") or ""
    if not protocol:
        return {"success": False, "error": "No protocol specified in device context"}
    fam = FAMILIES.get(protocol)
    if fam is None:
        return {"success": False, "error": f"Device protocol '{protocol}' not found"}
    entity_id = context.get("entity_id") or ""
    if context.get("color") and not context.get("rgb"):
        # A spoken colour also as rgb, for the families that only take rgb (LIFX, Kasa), as
        # N:'s control_device does.
        try:
            from jarvis_command_sdk import resolve_color
        except ImportError:  # an SDK before the shared palette
            resolve_color = None
        rgb = resolve_color(context["color"]) if resolve_color else None
        if rgb is not None:
            context = {**context, "rgb": list(rgb)}
    dev = DiscoveredDevice(entity_id=entity_id, name=context.get("name") or entity_id,
                           domain=context.get("domain") or "switch", manufacturer=protocol,
                           model=context.get("model") or "", protocol=protocol, cloud_id=context.get("cloud_id"),
                           local_ip=context.get("local_ip"), mac_address=context.get("mac_address"))
    try:
        result = _control_loop().run_until_complete(fam.control(dev, action_name, context))
    except Exception as exc:
        print(f"sidecar: {protocol}: control {action_name} failed for {entity_id}: {exc}", file=sys.stderr)
        return {"success": False, "error": str(exc)}
    out = {"success": bool(result.success), "error": None if result.success else result.error}
    if result.input_required:
        out["input_required"] = result.input_required.to_dict()
    return out


def action(commands: dict, params: dict) -> dict:
    """{success, error, input_required?, message} for a command's handle_action, as the
    requesting user; control_device with no such command (or one the household turned off) is
    the device family's control. message is what N: speaks for a voice-originated action."""
    name = params.get("command_name") or ""
    action_name = params.get("action_name") or ""
    context = params.get("context") or {}
    cmd = commands.get(name) if params.get("enabled", True) else None
    if cmd is None and name == "control_device":
        return _device_control(action_name, context)
    if cmd is None:
        return {"success": False, "error": f"Command '{name}' not found on this node"}
    set_current_user_id(params.get("user_id"))
    try:
        resp = cmd.handle_action(action_name, context)
    except Exception as exc:
        print(f"sidecar: {name}: action {action_name} failed: {exc}", file=sys.stderr)
        return {"success": False, "error": str(exc)}
    finally:
        set_current_user_id(None)
    data = resp.context_data or {}
    error = data.get("error")
    if not resp.success and resp.error_details:
        error = resp.error_details
    out = {"success": bool(resp.success), "error": error, "message": data.get("message") or ""}
    if data.get("input_required"):
        out["input_required"] = data["input_required"]
    return out


def callback(commands: dict, params: dict) -> dict:
    """{success, error, context_data?} for a command's @callback, as the requesting user; every
    failure is a result too (N:'s messages), so the inbox item doesn't wait forever."""
    name = params.get("command_name") or ""
    cb_name = params.get("callback_name") or ""
    cmd = commands.get(name) if params.get("enabled", True) else None
    if cmd is None:
        return {"success": False, "error": f"Command '{name}' not found on this node"}
    get_callbacks = getattr(cmd, "get_callbacks", None)
    if not callable(get_callbacks):
        return {"success": False, "error": f"Command '{name}' does not support callbacks"}
    try:
        callbacks = get_callbacks()
    except Exception as exc:
        return {"success": False, "error": f"get_callbacks failed: {exc}"}
    fn = callbacks.get(cb_name)
    if fn is None:
        return {"success": False, "error": f"Callback '{cb_name}' not registered on '{name}'"}
    uid = params.get("user_id")
    set_current_user_id(uid)
    try:
        ri = RequestInformation(voice_command=params.get("voice_command") or f"cb:{cb_name}",
                                conversation_id=params.get("conversation_id") or "callback", user_id=uid)
        resp = fn(params.get("data") or {}, ri)
    except Exception as exc:
        print(f"sidecar: {name}: callback {cb_name} failed: {exc}", file=sys.stderr)
        return {"success": False, "error": str(exc)}
    finally:
        set_current_user_id(None)
    error = (resp.context_data or {}).get("error")
    if not resp.success and resp.error_details:
        error = resp.error_details
    out = {"success": bool(resp.success), "error": error}
    if resp.context_data is not None:
        out["context_data"] = resp.context_data
    return out


DEVICE_METHODS = {"display_media": InvokeDisplayMedia, "device_scan": device_scan, "device_list": device_list, "device_state": device_state,
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
            elif method == "action":
                reply = {"id": msg_id, "result": action(commands, msg.get("params") or {})}
            elif method == "callback":
                reply = {"id": msg_id, "result": callback(commands, msg.get("params") or {})}
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
            elif method == "context_query":
                reply = {"id": msg_id, "result": context_query(commands, msg.get("params") or {})}
            elif method == "agent_run":
                reply = {"id": msg_id, "result": agent_run(msg.get("params") or {})}
            elif method == "reminders_due":
                if "reminder" not in commands:
                    raise RuntimeError("no built-in reminder command in this sidecar")
                reply = {"id": msg_id, "result": reminders_due()}
            else:
                reply = {"id": msg_id, "error": f"unknown method {method!r}"}
        except Exception as exc:
            reply = {"id": msg_id, "error": f"{type(exc).__name__}: {exc}"}
        PROTO.write(json.dumps(reply, default=str) + "\n")
        PROTO.flush()


if __name__ == "__main__":
    main()
