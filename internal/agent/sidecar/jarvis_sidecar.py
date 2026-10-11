"""Jarvis package sidecar (spike).

Loads IJarvisCommand classes from a directory and answers JSON-lines requests on stdin:

    -> {"id": 1, "method": "describe"}
    <- {"id": 1, "result": {"tools": [{"name", "client_tool", "command"}, ...]}}
    -> {"id": 2, "method": "call", "params": {"command_name", "arguments", "request": {...}}}
    <- {"id": 2, "result": {"output": {...}, "signals": {"wait_for_input", "clear_history",
                                                          "on_response_complete"?}}}
    -> {"id": 3, "method": "complete", "params": {"token": "1"}}
    <- {"id": 3, "result": {"ran": true}}
    <- {"id": N, "error": "..."}            on a protocol-level failure

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


def describe(commands: dict) -> dict:
    tools = []
    for name, cmd in commands.items():
        try:
            tools.append({"name": name, "client_tool": cmd.to_openai_tool_schema(), "command": cmd.get_command_schema()})
        except Exception as exc:
            print(f"sidecar: skipping {name}: {exc}", file=sys.stderr)
    return {"tools": tools}


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
    try:
        resp = cmd.execute(ri, **(params.get("arguments") or {}))
    except Exception as exc:
        return {"output": {"success": False, "error": str(exc)}}
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
    commands = load_commands(sys.argv[1])
    print(f"sidecar: loaded {sorted(commands)}", file=sys.stderr)
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
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
            else:
                reply = {"id": msg_id, "error": f"unknown method {method!r}"}
        except Exception as exc:
            reply = {"id": msg_id, "error": f"{type(exc).__name__}: {exc}"}
        PROTO.write(json.dumps(reply, default=str) + "\n")
        PROTO.flush()


if __name__ == "__main__":
    main()
