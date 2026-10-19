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
    return found


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
    out = []
    try:
        declared = cmd.required_secrets or []
    except Exception as exc:
        print(f"sidecar: {cmd.command_name}: required_secrets failed: {exc}", file=sys.stderr)
        return out
    for secret in declared:
        try:
            out.append({f: getattr(secret, f, None) for f in _SECRET_FIELDS})
        except Exception as exc:
            print(f"sidecar: {cmd.command_name}: skipping a secret: {exc}", file=sys.stderr)
    return out


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


def describe(commands: dict) -> dict:
    tools = []
    for name, cmd in commands.items():
        try:
            tools.append({"name": name, "client_tool": cmd.to_openai_tool_schema(), "command": cmd.get_command_schema(),
                          "secrets": _secrets(cmd), "data_browser": _browser(cmd),
                          "associated_service": _optional(cmd, "associated_service"),
                          "setup_guide": _optional(cmd, "setup_guide"), "package": PACKAGE_OF.get(name),
                          "context_operations": _context_ops(cmd)})
        except Exception as exc:
            print(f"sidecar: skipping {name}: {exc}", file=sys.stderr)
            if name in PACKAGE_OF:
                FAILED[PACKAGE_OF[name]] = f"{name}: {type(exc).__name__}: {exc}"
    return {"tools": tools, "failed": FAILED}


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
            elif method == "context_query":
                reply = {"id": msg_id, "result": context_query(commands, msg.get("params") or {})}
            else:
                reply = {"id": msg_id, "error": f"unknown method {method!r}"}
        except Exception as exc:
            reply = {"id": msg_id, "error": f"{type(exc).__name__}: {exc}"}
        PROTO.write(json.dumps(reply, default=str) + "\n")
        PROTO.flush()


if __name__ == "__main__":
    main()
