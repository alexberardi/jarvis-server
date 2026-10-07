"""Export the command-center golden fixtures that export_prompts.py / export_dates.py leave out.

    TZ=UTC ../jarvis-command-center/.venv/bin/python tools/golden/export_cc_extras.py \
        --cc ../jarvis-command-center --out fixtures/golden

Writes:

- prompts/_persona.json        persona_presets constants (PERSONA_FRAME, DEFAULT_PERSONA, presets, cap)
- prompts/_recently_shown.json core_rules.render_referenced_items_block over a small corpus
- prompts/_toolparse.json      ToolCallParser.parse_response (02 §9 item 2), tool-call ids masked
- dates/iso_guard.json         G4: the engine's _try_fix_iso_dates + is_iso_datetime matrix

Like the other exporters this lives in jarvis-server because the Python repos are frozen.
_try_fix_iso_dates is a closure inside ToolExecutionEngine.execute, so its source (and the
_replace_datetimes helper it calls) is cut out of the real method with inspect and executed
verbatim with the closure variable (timezone_str) bound: the code under test is the legacy text.
"""

import argparse
import inspect
import json
import os
import sys
import textwrap
from datetime import datetime as real_datetime
from pathlib import Path

ISO_INSTANTS = [
    "2026-10-05T14:30:00Z",  # Monday
    "2026-10-10T17:00:00Z",  # Saturday
    "2026-10-11T23:59:00Z",  # Sunday
]
ISO_ZONES = ["UTC", "America/New_York"]


def frozen_datetime(instant: real_datetime):
    class Frozen(real_datetime):
        @classmethod
        def now(cls, tz=None):
            if tz is None:
                return instant.replace(tzinfo=None)  # process TZ is UTC
            return instant.astimezone(tz)

        @classmethod
        def utcnow(cls):
            return instant.replace(tzinfo=None)

    return Frozen


def closure_source(method, name: str) -> str:
    """Return the source of the nested def `name` inside `method`, dedented."""
    src = textwrap.dedent(inspect.getsource(method))
    lines = src.splitlines()
    start = next(i for i, l in enumerate(lines) if l.lstrip().startswith(f"def {name}("))
    indent = len(lines[start]) - len(lines[start].lstrip())
    end = start + 1
    while end < len(lines):
        l = lines[end]
        if l.strip() and (len(l) - len(l.lstrip())) <= indent:
            break
        end += 1
    return textwrap.dedent("\n".join(lines[start:end]))


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--cc", required=True, type=Path)
    ap.add_argument("--out", required=True, type=Path)
    args = ap.parse_args()
    sys.path.insert(0, str(args.cc.resolve()))
    os.environ.setdefault("LOG_FULL_SYSTEM_PROMPT", "false")

    from app.core import date_resolution, general_context, param_validation
    from app.core.prompt_providers.shared import core_rules
    from app.core.tool_call_parser import ToolCallParser
    from app.core.tool_execution_engine import ToolExecutionEngine
    from app.services import persona_presets

    out = args.out
    (out / "prompts").mkdir(parents=True, exist_ok=True)
    (out / "dates").mkdir(parents=True, exist_ok=True)

    # --- persona constants ---
    persona = {
        "PERSONA_FRAME": persona_presets.PERSONA_FRAME,
        "DEFAULT_PERSONA": persona_presets.DEFAULT_PERSONA,
        "PERSONA_PRESETS": persona_presets.PERSONA_PRESETS,
        "PERSONA_MAX_CHARS": persona_presets.PERSONA_MAX_CHARS,
        "DEFAULT_PERSONA_PRESET_ID": persona_presets.DEFAULT_PERSONA_PRESET_ID,
    }
    (out / "prompts" / "_persona.json").write_text(json.dumps(persona, indent=1, ensure_ascii=False) + "\n")

    # --- recently shown block ---
    item = lambda i, actions: {"ref_id": f"r{i}", "label": f"Email {i} from abc — “subject”", "attrs": {}, "actions": actions}
    shown_cases = {
        "empty": [],
        "one": [item(1, ["archive"])],
        "no_actions": [item(1, []), item(2, None)],
        "three_mixed_actions": [item(1, ["reply", "archive"]), item(2, ["archive", "delete"]), item(3, [])],
        "ten_truncated": [item(i, ["archive"]) for i in range(1, 11)],
        "non_dict_item": [item(1, ["archive"]), "junk", item(3, ["star"])],
        "missing_fields": [{"actions": ["b", "a"]}],
    }
    shown = [{"name": k, "items": v, "block": core_rules.render_referenced_items_block(v)} for k, v in shown_cases.items()]
    (out / "prompts" / "_recently_shown.json").write_text(json.dumps(shown, indent=1, ensure_ascii=False) + "\n")

    # --- ToolCallParser.parse_response ---
    raws = [
        '{"message": "Hi", "tool_calls": []}',
        '{"message": "On it", "tool_calls": [{"name": "set_timer", "arguments": {"duration_seconds": 300}}]}',
        '{"message": "", "tool_call": {"name": "get_weather", "arguments": {}}}',
        '{"name": "quick_search", "arguments": {"query": "weather"}}',
        '{"name": "quick_search", "arguments": {"query": "x"}, "failure_message": "Search failed."}',
        '{"message": "", "tool_calls": [{"function": {"name": "get_weather", "arguments": "{\\"city\\": \\"Paris\\"}"}}]}',
        '{"message": "", "tool_calls": [{"name": "get_weather", "arguments": "{not json"}]}',
        '{"message": "", "tool_calls": [{"name": "get_weather", "arguments": null}]}',
        '{"message": "", "tool_calls": [{"name": "get_weather", "arguments": [1, 2]}]}',
        '{"message": "", "tool_calls": [{"name": "get_weather", "arguments": {"resolved_datetimes": ["Next Week", " TOMORROW:MORNING ", {"resolved_datetimes": ["This Weekend"]}, {"resolved_datetimes": "Tonight"}, {"other": 1}, 5]}}]}',
        '{"message": "", "tool_calls": [{"name": "get_weather", "arguments": {"resolved_datetimes": "today"}}]}',
        '{"message": "", "tool_calls": [{"name": "x", "arguments": {}, "failure_message": ""}, {"name": "y", "arguments": {}, "failure_message": 5}]}',
        '{\n  "message": "Done", # a comment\n  "tool_calls": [] # trailing\n}',
        '{"message": "Use # inside strings", "tool_calls": []}',
        'Sure! Here you go: {"message": "Hi", "tool_calls": []} hope that helps',
        'first {"broken": } then {"message": "second", "tool_calls": []}',
        'prefix {"a": {"b": 1}} and {"message": "x"}',
        'text {"message": "x", "tool_calls": [] trailing } garbage }',
        'no json here at all',
        '[1, 2, 3]',
        '"just a string"',
        '42',
        '{"message": null, "tool_calls": []}',
        '{"message": 7}',
        '{"tool_calls": [{"function": "not a dict"}]}',
        '{"tool_calls": [{"name": "", "arguments": {}}, {"no_name": true}, "str", {"name": "ok", "arguments": {"é": "ü \\ud83d\\ude00 <&>"}}]}',
        '{"tool_calls": {"name": "dict_not_list"}}',
        '{"tool_call": null, "message": "m"}',
        '{"message": "x", "tool_calls": [], "name": "would_be_bare", "arguments": {}}',
        '{"name": "bare_no_args"}',
        '{"function": {"name": "openai_bare", "arguments": {"a": 1.5, "b": 10000000000000000000000, "c": true}}}',
        '',
        '   ',
        '{"message": "unterminated',
        '```json\n{"message": "fenced", "tool_calls": [{"name": "t", "arguments": {}}]}\n```',
        '{"message": "esc \\"quote\\" { brace", "tool_calls": []}',
        '{"message": "dup", "message": "last wins", "tool_calls": []}',
        '{"message": "nan", "tool_calls": [{"name": "t", "arguments": {"v": NaN, "w": -Infinity}}]}',
    ]

    def mask(calls):
        masked = []
        for c in calls:
            c = dict(c)
            assert c["id"].startswith("call_") and len(c["id"]) == 17
            c["id"] = "call_MASKED"
            masked.append(c)
        return masked

    toolparse = []
    for r in raws:
        fr, calls, msg = ToolCallParser.parse_response(r)
        toolparse.append({"raw": r, "finish_reason": fr, "tool_calls": mask(calls), "message": msg})
    (out / "prompts" / "_toolparse.json").write_text(json.dumps(toolparse, indent=1, ensure_ascii=False) + "\n")

    # --- G4: ISO guard ---
    ns = {
        "json": json,
        "List": list, "Dict": dict, "Any": object,
        "generate_date_context_object": general_context.generate_date_context_object,
        "flatten_date_context": date_resolution.flatten_date_context,
        "is_iso_datetime": param_validation.is_iso_datetime,
    }
    from typing import Any, Dict, List  # noqa: E402  (the closures' annotations)
    ns.update({"List": List, "Dict": Dict, "Any": Any})
    exec(closure_source(ToolExecutionEngine.execute, "_replace_datetimes"), ns)
    exec(closure_source(ToolExecutionEngine.execute, "_try_fix_iso_dates"), ns)

    iso_cases = []
    for iso in ISO_INSTANTS:
        instant = real_datetime.fromisoformat(iso.replace("Z", "+00:00"))
        frozen = frozen_datetime(instant)
        general_context.datetime = frozen
        date_resolution.datetime = frozen
        for zone in ISO_ZONES:
            ns["timezone_str"] = zone
            ctx = general_context.generate_date_context_object(zone)
            flat = date_resolution.flatten_date_context(ctx)
            today = flat["today"]
            tomorrow = flat["tomorrow"]
            weekend = flat["this_weekend"]
            month = flat["this_month"]
            call = lambda a: {"id": "call_1", "type": "function", "function": {"name": "get_weather", "arguments": a}}
            dumps = json.dumps
            cases = {
                "clean_keys": [call(dumps({"resolved_datetimes": ["today"]}))],
                "clean_no_param": [call(dumps({"city": "Paris"}))],
                "clean_not_iso": [call(dumps({"resolved_datetimes": ["2026-10-05", "2026-10-05T10:00:00"]}))],
                "fixed_single": [call(dumps({"city": "Paris", "resolved_datetimes": [today]}))],
                "fixed_single_string": [call(dumps({"resolved_datetimes": tomorrow}))],
                "fixed_two_singles": [call(dumps({"resolved_datetimes": [tomorrow, today]}))],
                "fixed_multi_weekend": [call(dumps({"resolved_datetimes": list(reversed(weekend))}))],
                "fixed_multi_month": [call(dumps({"resolved_datetimes": month}))],
                "fixed_mixed_key_and_iso": [call(dumps({"resolved_datetimes": ["today", tomorrow]}))],
                "fixed_saturday_alone": [call(dumps({"resolved_datetimes": [weekend[0]]}))],
                "fixed_non_ascii_args": [call(dumps({"label": "café ☕", "resolved_datetimes": [today]}))],
                "bad_unknown_iso": [call(dumps({"resolved_datetimes": ["2019-03-04T05:06:07Z"]}))],
                "bad_partial": [call(dumps({"resolved_datetimes": [today, "2019-03-04T05:06:07Z"]}))],
                "bad_and_fixed": [call(dumps({"resolved_datetimes": [today]})), call(dumps({"resolved_datetimes": ["2019-03-04T05:06:07+02:00"]}))],
                "skip_bad_json": [call("{nope"), call(dumps({"resolved_datetimes": [today]}))],
                "skip_non_dict": [call("[1]"), call(dumps({"resolved_datetimes": 5}))],
            }
            for name, calls in cases.items():
                before = json.loads(json.dumps(calls))
                status = ns["_try_fix_iso_dates"](calls)
                iso_cases.append({"now": iso, "timezone": zone, "name": name, "calls": before,
                                  "status": status, "after": calls})

    iso_values = [
        "2025-01-15T10:30:00Z", "2025-01-15T10:30:00+00:00", "2025-01-15T10:30:00+05:30",
        "2025-01-15T10:30:00-08:00", "2025-01-15", "2025-01-15T10:30:00", "not-a-datetime", "",
        "2025-01-15T10:30Z", "2025-01-15T10Z", "2025-01-15T10:30:00.123456Z", "2025-01-15T10:30:00.1Z",
        "2025-01-15T10:30:00.123+01:00", "20250115T103000Z", "2025-01-15T103000+0530",
        "2025-01-15 10:30:00Z", "2025-01-15T24:00:00Z", "2025-02-30T10:00:00Z", "2025-01-15T10:30:00+24:00",
        "2025-01-15T10:30:00+05", "2025-01-15T10:30:00+0530", "2025-01-15T10:30:00z", "T", "2025-W03-3T10:00:00Z",
        "2025-01-15T10:30:00+05:30:15", "2025-01-15T10:30:60Z", "2025-01-15T1:30:00Z", "Tomorrow", "2025-01-15T10:30:00ZZ",
        "2025-01-15T10:30:00 Z", "+2025-01-15T10:30:00Z", "2025-001T10:00:00Z", "2025-01-15T10:30:00,5Z",
        "２０２５-01-15T10:30:00Z", "2025-01-15T10:30:00-00:00", "0001-01-01T00:00:00Z", "9999-12-31T23:59:59Z",
    ]
    iso_matrix = [{"value": v, "is_iso": param_validation.is_iso_datetime(v)} for v in iso_values]
    (out / "dates" / "iso_guard.json").write_text(json.dumps(
        {"python": sys.version.split()[0], "is_iso_datetime": iso_matrix, "try_fix_iso_dates": iso_cases},
        indent=1, ensure_ascii=False) + "\n")

    print(f"persona, {len(shown)} shown, {len(toolparse)} toolparse, {len(iso_cases)} iso guard, {len(iso_matrix)} iso values -> {out}")


if __name__ == "__main__":
    main()
