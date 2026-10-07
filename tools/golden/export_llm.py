"""Export G6 (date keys) and G7 (JSON shaping) fixtures from the legacy llm-proxy.

Runs on Python 3.11, the version prod's llm-proxy image runs (its Dockerfile pins python3.11).
That matters: 3.13 changed json's trailing-comma errors, which steer the JSON repair cascade.
Only the standard library is needed; the llm-proxy's heavy imports are stubbed.

    mise exec python@3.11 -- python tools/golden/export_llm.py \
        --llm ../jarvis-llm-proxy-api --out fixtures/golden/llm

Writes (see fixtures/golden/README.md, "llm/"):
    date_keys_corpus.jsonl  every row of data/jarvis_training.jsonl with the matcher's keys
    date_keys_edge.json     edge inputs (false positives, negatives, non-ASCII, CC-style text)
    vocabulary.json         GET /v1/adapters/date-keys (adapter_trained pinned to false)
    pyjson.json             json.loads/json.dumps over a malformed-JSON corpus, plus float repr
    json_parse.json         parse_json_response and each repair helper over the same corpus
    json_inject.json        inject_json_system_message
    json_schema.json        validate_json_schema and summarize_json_schema
    json_retry.json         fix_json_with_retry: the correction turn and retry parameters
"""

import argparse
import asyncio
import json
import sys
import types
from pathlib import Path


def stub_modules() -> None:
    """Stand-ins for the llm-proxy imports chat_runner needs (fastapi, pydantic models, httpx)."""

    class HTTPException(Exception):
        def __init__(self, status_code, detail=None):
            super().__init__(detail)
            self.status_code = status_code
            self.detail = detail

    class BackendHTTPError(Exception):
        def __init__(self, status_code, message):
            super().__init__(f"HTTP {status_code}: {message}")
            self.status_code = status_code
            self.upstream_message = message

    mods = {
        "fastapi": {"HTTPException": HTTPException},
        "backends": {},
        "backends.rest_backend": {"BackendHTTPError": BackendHTTPError},
        "models": {},
        "models.api_models": {"ChatCompletionRequest": object, "Message": object},
        "services": {},
        "services.response_helpers": {"error_type_for_status": lambda s: "x"},
        "services.settings_helpers": {"resolve_slot_reasoning_budget": lambda m: None},
    }
    for name, attrs in mods.items():
        m = types.ModuleType(name)
        m.__path__ = []  # packages
        for k, v in attrs.items():
            setattr(m, k, v)
        sys.modules[name] = m


def load_module(name: str, path: Path):
    import importlib.util

    spec = importlib.util.spec_from_file_location(name, path)
    mod = importlib.util.module_from_spec(spec)
    sys.modules[name] = mod
    spec.loader.exec_module(mod)
    return mod


# --- G6 -------------------------------------------------------------------------------------

DATE_EDGE = [
    "", "   ", "\n\t", "call Tom", "the sun is out", "I sat on it", "Christmas eve", "we wed in june",
    "Can we talk tom?", "last wed aft", "set a timer for 10 minutes", "play Yesterday",
    "Play Saturday Night Live", "watch the game on sunday", "night shift starts tomorrow",
    "I waited 20 minutes", "it lasted 3 hours", "for 5 minutes from now", "for 10 minutes.",
    "remind me later", "in a few minutes", "in a bit", "expires in 3 days", "about 2 hours long",
    "in about 2 hours long", "who's playing tonight", "date night next friday",
    "remind me in 2 hours and 15 minutes", "in two hours and 30 minutes", "in two and a half hours",
    "in an hour and a half", "in an hour and 20 minutes", "in a couple of hours", "in half an hour",
    "in a quarter of an hour", "an hour from now", "3 hours from now", "in 45 minutes",
    "in forty-five minutes", "in a couple of days", "in 2 days", "in 10 days", "in a week",
    "in 3 weeks", "quarter past 3pm", "half past 11 am", "quarter to 1pm", "quarter to 5 PM",
    "at 12 am", "12:00 pm", "at 7:30pm", "at 7:00 am", "9am", "9 thirty pm", "10 forty five am",
    "12 o'clock", "at noon tomorrow", "midnight snack", "tomorrow morning at 8am",
    "the day after tomorrow", "day before yesterday", "2 days ago", "next tues", "the following sat",
    "this weekend", "next weekend", "last month", "after dinner", "post-dinner", "over lunch",
    "TOMORROW NIGHT", "Tomorrow:Morning", "tomorrow,morning", "tomorrow_morning", "tomorrows",
    "remind me tomorrow morning to call mom", "what's on my calendar for next monday afternoon?",
    "turn on the lights /no_think",
    "Remind me tomorrow.\n\n[Turn hint: continue]\nAgent context: calendar says lunch on friday /no_think",
    "mañana tomorrow", "café at 9am", "tomorrow morning", "tomorrow morning",
    "Tomorrow İs fine", "tomorrow ıs", "the ſun", "in ٣ hours", "in ٢ days", "at ٩am",
    "in 2 hours", "toḿorrow", "toḿ", "tomorrow²", "x_tomorrow", "tomorrow_",
    "tonight\n", "later tonight", "this evening late", "in 1 hour", "in 90 minutes", "in 007 minutes",
    "in 00 hours", "on monday blues", "taco tuesday", "monday motivation please",
    "set timer for 5 minutes tomorrow", "listen to morning edition", "put on night music for an hour",
]


def export_dates(llm: Path, out: Path) -> None:
    m = load_module("date_key_matcher", llm / "services" / "date_key_matcher.py")
    rows, bad = [], 0
    with open(llm / "data" / "jarvis_training.jsonl", encoding="utf-8") as f:
        for line in f:
            row = json.loads(line)
            got = m.extract_date_keys(row["text"])
            if got != sorted(row["date_keys"]):
                bad += 1
            rows.append({"text": row["text"], "date_keys": got})
    with open(out / "date_keys_corpus.jsonl", "w", encoding="utf-8") as f:
        for r in rows:
            f.write(json.dumps(r, ensure_ascii=False) + "\n")
    edge = [{"text": t, "date_keys": m.extract_date_keys(t)} for t in DATE_EDGE]
    (out / "date_keys_edge.json").write_text(json.dumps(edge, indent=1, ensure_ascii=False) + "\n", encoding="utf-8")
    print(f"G6: {len(rows)} corpus rows ({bad} differ from their labels), {len(edge)} edge rows")

    src = (llm / "services" / "date_keys.py").read_text()
    head = src[: src.index("\ndef get_adapter_path")]
    head = "\n".join(l for l in head.splitlines() if not l.startswith("from services"))
    ns: dict = {}
    exec(compile(head, "date_keys_head", "exec"), ns)
    vocab = ns["get_date_keys_response"]()
    vocab["adapter_trained"] = False
    (out / "vocabulary.json").write_text(json.dumps(vocab, indent=1) + "\n")


# --- G7 -------------------------------------------------------------------------------------

JSON_CORPUS = [
    '{"a":1}', '{"a": 1}', '{\n  "a": 1,\n  "b": [1, 2]\n}', '[1,2,3]', '"just a string"', "42", "null",
    "true", "  {\"a\": 1}  \n", '{"a": 1, "a": 2}', '{"a": [1], "a": 2}', '{"a": {"b": 1, "b": 2}}',
    '[{"x": 1, "x": {"y": 1, "y": 2}}]', '{"a": "café"}', '{"a": "caf\\u00e9"}', '{"emoji": "\\ud83d\\ude00"}',
    '{"emoji": "\U0001F600"}', '{"lone": "\\ud800"}', '{"lone": "\\udc00x"}', '{"ctl": "a\\u0001b"}',
    '{"tab": "a\\tb\\nc\\"d\\\\e\\/f\\bg\\fh\\ri"}', '{"f": 1.0, "g": 1e20, "h": 1e-7, "i": 0.1, "j": -0.0}',
    '{"big": 123456789012345678901234567890, "e": 1E5, "neg": -12, "z": 0}', '{"inf": 1e400, "ninf": -1e400}',
    '[NaN, Infinity, -Infinity]', '{"f": 1e16, "g": 1e15, "h": 0.0001, "k": 0.00001, "l": 123456789.123456789}',
    '{"f": 2.5e-5, "g": 1.7976931348623157e308, "h": 5e-324, "i": 100.0, "j": 3.14159}',
    '{"a": 1,}', '[1, 2,]', '{"a": 1 "b": 2}', '{"a" 1}', "{a: 1}", "{'a': 1}", '{"a": "he said "hi""}',
    '{"text": "unterminated', '{"items": [{"name": "x"}, {"name": "y', '{"a": 1, "b": {"c": [1, 2',
    '{"a": 1, "b":', '{"a": 1, "b": ', '[1, 2, 3', '{"a": [1, 2, 3], "b": "x"', '{"a": "x",', '{"a": "x",  ',
    'Here is the JSON: {"a": 1} hope that helps', 'Sure!\n```json\n{"answer": "yes"}\n```',
    '```\n[1, 2]\n```', 'prefix [1, 2] and {"a": 1}', 'text {"a": 1} more {"b": 2} end', 'no json here',
    "", "   ", "{}", "[]", "{", "[", "}", "]", '{"a": tru}', '{"a": nul}', "01", "1.", "1e", "-", "-1",
    "1.5e+3", '{"a": 1}{"b": 2}', '{"a": 1} trailing', '﻿{"a": 1}', '{"a": "line\nbreak"}',
    '{"a": "\\x41"}', '{"a": "\\u12"}', '{"a": "\\u12zz"}', '"\\u0041', '{"k": "v", "k": "w", "k": "z"}',
    '{"reasoning": "<think>hmm</think>", "answer": 1}', '<think>\nlet me think\n</think>\n{"a": 1}',
    '{"name": "Mr. "Bob" Smith", "age": 30}', '{"msg": "it\'s fine"}', '{"a": [1, 2, 3}', '{"a": {"b": 1]}',
    '[{"a": 1}, {"b": 2}', '{"x": "a,b", "y": [1,2,],}', '{"deep": [[[[[1]]]]], "s": "]}"}',
    '{"unicode_key_é": "日本語"}', '{"a": 1}\n', '{"a":"x"}{"a":"y"}', '[1] [2]',
    '{"a": "b\\"c"}', '{"a": "\\"quoted\\""}', 'He said "hello" and left', '"a" "b"', '{"a": "x" "y"}',
]

SCHEMA_CASES = [
    ({"a": 1}, {}),
    ({"a": 1}, {"type": "object", "required": ["a", "b"]}),
    ({"a": "x"}, {"type": "object", "properties": {"a": {"type": "integer"}}}),
    ({"a": 1.5}, {"type": "object", "properties": {"a": {"type": "integer"}}}),
    ({"a": 1}, {"type": "object", "properties": {"a": {"type": "number"}}}),
    ({"a": True}, {"type": "object", "properties": {"a": {"type": "integer"}}}),
    ({"a": None}, {"type": "object", "properties": {"a": {"type": ["string", "null"]}}}),
    ({"a": 3}, {"type": "object", "properties": {"a": {"type": ["string", "null"]}}}),
    ([1, "x"], {"type": "array", "items": {"type": "integer"}}),
    ([1, 2], {"type": "array", "items": {"type": "integer"}}),
    ({"a": [{"b": 1}, {"c": 2}]}, {"type": "object", "properties": {"a": {"type": "array", "items": {"type": "object", "required": ["b"]}}}}),
    ("x", {"type": "object"}),
    ({"a": 1}, {"properties": {"a": {"type": "string"}}}),
    ([1], {"items": {"type": "string"}}),
    ({"a": 1}, {"type": "weird"}),
    ({"z": 1, "a": "x"}, {"type": "object", "properties": {"z": {"type": "string"}, "a": {"type": "integer"}}}),
    ({"a": 1}, {"type": "object", "required": "ab"}),
    ({"a": 1}, {"type": "object", "required": [1]}),
    ({}, {"type": "object", "required": ["x"], "properties": {"x": {"type": "string"}, "y": {}}}),
    ({"a": {}}, {"type": "object", "properties": {"a": {"type": "object", "properties": {"q": {"type": "boolean"}}, "required": ["q"]}}}),
    (None, {"type": "null"}),
    (1, {"type": ["object", "array"]}),
]

SUMMARY_SCHEMAS = [
    None, {}, {"type": "object"}, {"required": ["a", "b"]},
    {"type": "object", "properties": {"a": {"type": "string"}, "b": {}, "c": {"type": ["string", "null"]}}, "required": ["a"]},
    {"properties": {"x": {"type": "integer"}}}, {"properties": {}, "required": []},
    {"type": "object", "properties": {"facts": {"type": "array", "items": {"type": "string"}}}, "required": ["facts"]},
]


def parse_ordered(s):
    return json.loads(s)


def export_json(llm: Path, out: Path) -> None:
    stub_modules()
    sys.path.insert(0, str(llm))
    from managers.chat_types import GenerationParams, ChatResult, NormalizedMessage, TextPart, ImagePart
    cr = load_module("chat_runner", llm / "services" / "chat_runner.py")

    def call(fn, *a):
        try:
            return {"ok": fn(*a)}
        except Exception as e:  # noqa: BLE001
            return {"raises": f"{type(e).__name__}: {e}"}

    # pyjson: loads + dumps
    pj = []
    for s in JSON_CORPUS:
        row = {"input": s}
        try:
            v = json.loads(s)
            row["dumps"] = json.dumps(v, ensure_ascii=False)
            row["dumps_ascii"] = json.dumps(v)
        except json.JSONDecodeError as e:
            row["error"] = str(e)
        except Exception as e:  # noqa: BLE001
            row["raises"] = f"{type(e).__name__}: {e}"
        pj.append(row)
    floats = [0.0, -0.0, 1.0, 0.1, 1e16, 1e15, 1e-4, 1e-5, 123456789012345678.0, 1.5e-7, 2.5, 1e22, 1e100,
              5e-324, 1.7976931348623157e308, 0.30000000000000004, 100.0, 1234567.0, 9999999999999998.0,
              float("inf"), float("-inf")]
    (out / "pyjson.json").write_text(json.dumps({
        "loads": pj,
        "float_repr": [{"repr": repr(f), "json": json.dumps(f), "hex": f.hex()} for f in floats],
    }, indent=1, ensure_ascii=True) + "\n", encoding="utf-8")

    rows = []
    for s in JSON_CORPUS:
        content, valid = cr.parse_json_response(s)
        rows.append({
            "input": s,
            "parse_json_response": {"content": content, "valid": valid},
            "repair_duplicate_keys": cr.repair_duplicate_keys(s),
            "repair_unescaped_quotes": cr.repair_unescaped_quotes(s),
            "extract_json_from_text": cr.extract_json_from_text(s),
            "repair_truncated_json": cr.repair_truncated_json(s),
            "is_json_truncated": cr.is_json_truncated(s),
        })
    (out / "json_parse.json").write_text(json.dumps(rows, indent=1, ensure_ascii=True) + "\n", encoding="utf-8")

    def msgs(spec):
        out_ = []
        for role, parts in spec:
            if isinstance(parts, str):
                parts = [parts]
            out_.append(NormalizedMessage(role=role, content=[TextPart(text=p) for p in parts]))
        return out_

    inject_cases = [
        [("user", "hi")],
        [("system", "You are Jarvis."), ("user", "hi")],
        [("system", "Respond in JSON."), ("user", "hi")],
        [("system", "Always return valid JSON only."), ("user", "hi")],
        [("system", "  valid json please  "), ("user", "hi")],
        [("system", "First."), ("user", "hi"), ("system", "Second, valid JSON.")],
        [("system", ["part one", "part two"]), ("user", "x")],
        [("system", []), ("user", "x")],
        [("system", "\n\nYou are helpful.\n\n"), ("assistant", "ok"), ("user", "go")],
    ]
    inj = []
    for spec in inject_cases:
        res = cr.inject_json_system_message(msgs(spec))
        inj.append({
            "input": [{"role": r, "parts": p if isinstance(p, list) else [p]} for r, p in spec],
            "output": [{"role": m.role, "parts": [p.text for p in m.content]} for m in res],
        })
    (out / "json_inject.json").write_text(json.dumps(inj, indent=1, ensure_ascii=True) + "\n", encoding="utf-8")

    val = [{"value": v, "schema": sc, "result": call(cr.validate_json_schema, v, sc)} for v, sc in SCHEMA_CASES]
    summ = [{"schema": sc, "result": call(cr.summarize_json_schema, sc)} for sc in SUMMARY_SCHEMAS]
    trunc = [{"input": s, "result": cr.is_json_truncated(s)} for s in JSON_CORPUS]
    (out / "json_schema.json").write_text(json.dumps({"validate": val, "summarize": summ, "is_json_truncated": trunc},
                                                     indent=1, ensure_ascii=True) + "\n", encoding="utf-8")

    retry_cases = [
        {"invalid": '{"a": 1', "schema": None, "reply": '{"a": 1}', "temperature": 0.7, "max_tokens": None, "reasoning_budget": None},
        {"invalid": "no json", "schema": {"type": "object", "required": ["facts"], "properties": {"facts": {"type": "array"}}},
         "reply": '{"facts": []}', "temperature": 0.0, "max_tokens": 600, "reasoning_budget": 0},
        {"invalid": "x" * 3000, "schema": {"required": ["a"]}, "reply": "still bad", "temperature": 0.4, "max_tokens": 9000, "reasoning_budget": -1},
        {"invalid": '{"facts": "nope"}', "schema": {"type": "object", "properties": {"facts": {"type": "array"}}},
         "reply": '{"facts": "nope"}', "temperature": 0.3, "max_tokens": 0, "reasoning_budget": None},
        {"invalid": "café " * 400, "schema": None, "reply": '```json\n{"a": "é"}\n```', "temperature": 1.0, "max_tokens": 100, "reasoning_budget": 2},
    ]

    class Backend:
        def __init__(self, reply):
            self.reply = reply
            self.seen = None

        def generate_text_chat(self, cfg, messages, params):
            self.seen = (messages, params)
            return ChatResult(content=self.reply)

    retries = []
    for c in retry_cases:
        b = Backend(c["reply"])
        base = msgs([("system", "sys"), ("user", "question")])
        params = GenerationParams(temperature=c["temperature"], max_tokens=c["max_tokens"], top_p=0.9, seed=7,
                                  reasoning_budget=c["reasoning_budget"], response_format={"type": "json_object"})
        res = asyncio.run(cr.fix_json_with_retry(b, None, base, params, c["invalid"], c["schema"], max_retries=1))
        m, p = b.seen
        retries.append({
            "case": c,
            "result": res,
            "messages": [{"role": x.role, "parts": [t.text for t in x.content]} for x in m],
            "params": {"temperature": p.temperature, "max_tokens": p.max_tokens, "top_p": p.top_p, "seed": p.seed,
                       "reasoning_budget": p.reasoning_budget},
        })
    (out / "json_retry.json").write_text(json.dumps(retries, indent=1, ensure_ascii=True) + "\n", encoding="utf-8")
    print(f"G7: {len(JSON_CORPUS)} JSON inputs, {len(inj)} inject, {len(val)} validate, {len(retries)} retry cases")


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--llm", required=True, type=Path)
    ap.add_argument("--out", required=True, type=Path)
    args = ap.parse_args()
    if sys.version_info[:2] != (3, 11):
        sys.exit(f"run on Python 3.11 (prod's llm-proxy version), not {sys.version.split()[0]}")
    args.out.mkdir(parents=True, exist_ok=True)
    export_dates(args.llm, args.out)
    export_json(args.llm, args.out)


if __name__ == "__main__":
    main()
