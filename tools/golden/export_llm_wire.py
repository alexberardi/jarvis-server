"""Export G10 (stream translation), G11 (response envelopes) and G12 (queue callback envelope)
fixtures from the legacy llm-proxy, using its own virtualenv (httpx, pydantic, fastapi).

    ../jarvis-llm-proxy-api/.venv/bin/python tools/golden/export_llm_wire.py \
        --llm ../jarvis-llm-proxy-api --out fixtures/golden/llm

Writes fixtures/golden/llm/wire.json:
    stream     upstream llama-server SSE transcripts → the Jarvis frames the legacy model service
               emitted (json.dumps of each RestClient.generate_text_chat_stream event), through a
               mock transport
    envelopes  create_openai_response(...).model_dump() for content, tool calls and usage cases
    error_types  error_type_for_status for a table of statuses
    callback   queues.tasks._build_callback_envelope key order and values
None of this depends on the json module's error text, so the venv's Python version is fine
(G6/G7 need 3.11; see export_llm.py).
"""

import argparse
import json
import sys
import types
from pathlib import Path

TRANSCRIPTS = {
    "content_only": [
        'data: {"choices":[{"index":0,"delta":{"role":"assistant","content":null}}]}',
        'data: {"choices":[{"index":0,"delta":{"content":"Hel"}}]}',
        'data: {"choices":[{"index":0,"delta":{"content":"lo"}}]}',
        'data: {"choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}',
        'data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}',
        "data: [DONE]",
    ],
    "with_usage": [
        'data: {"choices":[{"index":0,"delta":{"content":"One"}}]}',
        'data: {"choices":[{"index":0,"delta":{"content":", two"}}]}',
        'data: {"choices":[{"index":0,"delta":{},"finish_reason":"length"}]}',
        'data: {"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":2,"total_tokens":14}}',
        "data: [DONE]",
    ],
    "parallel_tool_calls": [
        'data: {"choices":[{"index":0,"delta":{"role":"assistant","content":null,"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"get_weather","arguments":""}}]}}]}',
        'data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"get_time","arguments":""}}]}}]}',
        'data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\\"city\\": "}}]}}]}',
        'data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\\"tz\\": \\"UTC\\"}"}}]}}]}',
        'data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\\"Paris\\"}"}}]}}]}',
        'data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":40,"completion_tokens":20,"total_tokens":60}}',
        "data: [DONE]",
    ],
    "malformed_frame": [
        'data: {"choices":[{"index":0,"delta":{"content":"a"}}]}',
        "data: {not json",
        'data: {"choices":[{"index":0,"delta":{"content":"b"}}]}',
        'data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}',
    ],
    "framing_noise": [
        ": keep-alive",
        "event: message",
        "",
        'data:{"choices":[{"index":0,"delta":{"content":"caf\\u00e9 \\u2014 \\ud83d\\ude00"}}]}',
        '  data: {"choices":[{"index":0,"delta":{"content":""}}]}  ',
        'data: {"choices":[{"index":0,"delta":{"content":"<think>\\n</think>\\n\\nok"}}]}',
        'data: {"choices":[{"index":0,"delta":{"content":"\\"quoted\\" \\\\ tab\\t"}}]}',
        'data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}',
        "data: [DONE]",
        "",
    ],
}


def stub_settings() -> None:
    m = types.ModuleType("services.settings_service")

    def get_settings_service():
        raise RuntimeError("no settings DB in the exporter")

    m.get_settings_service = get_settings_service
    sys.modules["services.settings_service"] = m


def export_stream(out: dict) -> None:
    import httpx
    from backends.rest_backend import RestClient
    from managers.chat_types import GenerationParams, NormalizedMessage, TextPart

    rows = []
    for name, lines in TRANSCRIPTS.items():
        body = ("\r\n" if name == "framing_noise" else "\n").join(lines) + "\n"

        def handler(request, body=body):
            return httpx.Response(200, content=body.encode(), headers={"content-type": "text/event-stream"})

        rc = RestClient("http://engine", "m", "live")
        rc.client = httpx.AsyncClient(transport=httpx.MockTransport(handler))
        frames = list(rc.generate_text_chat_stream(None, [NormalizedMessage("user", [TextPart("hi")])],
                                                   GenerationParams(temperature=0.0)))
        rows.append({"name": name, "upstream": body, "frames": [json.dumps(f) for f in frames]})
    out["stream"] = rows


def export_envelopes(out: dict) -> None:
    from services.response_helpers import create_openai_response, error_type_for_status

    cases = [
        {"content": "hello", "model_name": "live"},
        {"content": "", "model_name": "background", "usage": {"prompt_tokens": 3, "completion_tokens": 0, "total_tokens": 3},
         "date_keys": []},
        {"content": "x", "model_name": "live", "date_keys": ["tomorrow", "morning"], "finish_reason": "length"},
        {"content": "", "model_name": "live", "tool_calls": [
            {"id": "call_1", "type": "function", "function": {"name": "f", "arguments": "{\"a\": 1}"}}]},
        {"content": "", "model_name": "live", "tool_calls": [{"function": {"name": "g"}}], "finish_reason": "tool_calls"},
    ]
    rows = []
    for c in cases:
        resp = create_openai_response(**c).model_dump()
        for tc in resp["choices"][0]["message"].get("tool_calls") or []:
            if not c["tool_calls"][0].get("id"):
                tc["id"] = "<generated>"
        resp["id"], resp["created"] = "<id>", 0
        rows.append({"input": c, "output": resp})
    out["envelopes"] = rows
    out["error_types"] = {str(s): error_type_for_status(s) for s in (400, 401, 403, 404, 408, 413, 422, 429, 499, 500, 502, 503)}


def export_callback(out: dict) -> None:
    sys.modules.setdefault("redis", types.ModuleType("redis"))
    rq = types.ModuleType("rq")
    rq.Queue = object
    sys.modules.setdefault("rq", rq)
    from queues.tasks import _build_callback_envelope

    env = _build_callback_envelope(job_id="j1", job_type="chat", status="succeeded", result={"content": "{}"},
                                   error=None, timing={"processing_ms": 5}, metadata={"type": "memory", "n": 1})
    env["finished_at"] = "<YYYY-MM-DDTHH:MM:SSZ>"
    out["callback"] = {"keys": list(env), "envelope": env}


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--llm", required=True, type=Path)
    ap.add_argument("--out", required=True, type=Path)
    args = ap.parse_args()
    sys.path.insert(0, str(args.llm.resolve()))
    stub_settings()
    out: dict = {}
    export_stream(out)
    export_envelopes(out)
    export_callback(out)
    args.out.mkdir(parents=True, exist_ok=True)
    (args.out / "wire.json").write_text(json.dumps(out, indent=1) + "\n")
    print(f"G10: {len(out['stream'])} transcripts, G11: {len(out['envelopes'])} envelopes, G12: callback")


if __name__ == "__main__":
    main()
