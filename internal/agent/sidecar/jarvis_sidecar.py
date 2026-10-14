"""Jarvis package sidecar (spike).

Loads IJarvisCommand classes from a directory and answers JSON-lines requests on stdin:

    -> {"id": 1, "method": "describe"}
    <- {"id": 1, "result": {"tools": [{"name", "client_tool", "command"}, ...]}}
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

"signals" are the CommandResponse fields only the node acts on; they never reach
command-center. A command's on_response_complete callable (a media command's deferred "start
playing now") stays here under a token; the agent runs it with "complete" once the spoken reply
is over.

stdout carries protocol only: print() inside commands is redirected to stderr, which the Go
agent forwards to its log. The Go agent owns the node's identity, key and MQTT; this process
never sees them.
"""

import importlib.util
import inspect
import json
import pathlib
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


def load_commands(directory: str) -> dict:
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
        for _, cls in inspect.getmembers(module, inspect.isclass):
            if issubclass(cls, IJarvisCommand) and cls.__module__ == module.__name__ and not inspect.isabstract(cls):
                cmd = cls()
                found[cmd.command_name] = cmd
    return found


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


def describe(commands: dict) -> dict:
    tools = []
    for name, cmd in commands.items():
        try:
            tools.append({"name": name, "client_tool": cmd.to_openai_tool_schema(), "command": cmd.get_command_schema(),
                          "secrets": _secrets(cmd), "data_browser": _browser(cmd),
                          "associated_service": _optional(cmd, "associated_service"),
                          "setup_guide": _optional(cmd, "setup_guide")})
        except Exception as exc:
            print(f"sidecar: skipping {name}: {exc}", file=sys.stderr)
    return {"tools": tools}


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
            commands = load_commands(sys.argv[1])
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
            else:
                reply = {"id": msg_id, "error": f"unknown method {method!r}"}
        except Exception as exc:
            reply = {"id": msg_id, "error": f"{type(exc).__name__}: {exc}"}
        PROTO.write(json.dumps(reply, default=str) + "\n")
        PROTO.flush()


if __name__ == "__main__":
    main()
