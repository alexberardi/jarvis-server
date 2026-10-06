"""Export G1/G5 golden fixtures from the legacy command-center prompt providers.

Run with command-center's virtualenv, from anywhere:

    ../jarvis-command-center/.venv/bin/python tools/golden/export_prompts.py \
        --cc ../jarvis-command-center --out fixtures/golden/prompts

This lives in jarvis-server, not beside the Python code: the Python server repos are frozen
(hard stop). It imports the real providers and the real handler wrapper, so the output is
exactly what the legacy stack sends; Go must reproduce it byte for byte (docs/cc/03 §3.3).
"""

import argparse
import copy
import hashlib
import json
import os
import sys
import types
from pathlib import Path

HERE = Path(__file__).resolve().parent


def load_cc(cc_root: Path):
    sys.path.insert(0, str(cc_root))
    os.environ.setdefault("LOG_FULL_SYSTEM_PROMPT", "false")
    from app.core.conversation_handler import ConversationHandler
    from app.core.prompt_providers.large.untrained.chatgpt_openai import ChatGPTOpenAI
    from app.core.prompt_providers.large.untrained.qwen3_14b_compressed import Qwen3_14B_Compressed
    from app.core.prompt_providers.medium.untrained.qwen3_5_9b_compressed import Qwen3_5_9B_Compressed
    from app.core.prompt_providers.medium.untrained.qwen3_8b_compressed import Qwen3_8B_Compressed
    from app.core.prompt_providers.shared import core_rules
    from app.core.tool_registry import ToolRegistry
    from app.services.persona_presets import DEFAULT_PERSONA

    providers = [Qwen3_14B_Compressed, Qwen3_8B_Compressed, Qwen3_5_9B_Compressed, ChatGPTOpenAI]
    return ConversationHandler, providers, core_rules, ToolRegistry, DEFAULT_PERSONA


def wrap(ConversationHandler, provider, node_context, tools, flags, characterization_on):
    """Call the real ConversationHandler._get_system_prompt with a stub self."""
    stub = types.SimpleNamespace(
        prompt_provider=provider,
        model=None,
        _get_characterization_injection_enabled=lambda nc: characterization_on,
    )
    return ConversationHandler._get_system_prompt(stub, node_context, None, tools, flags)


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--cc", required=True, type=Path)
    ap.add_argument("--out", required=True, type=Path)
    args = ap.parse_args()

    ConversationHandler, providers, core_rules, ToolRegistry, DEFAULT_PERSONA = load_cc(args.cc.resolve())
    corpus = json.loads((HERE / "inputs" / "corpus.json").read_text())
    server_tools = ToolRegistry().get_tool_definitions()
    client_tools = corpus["client_tools"]

    tool_sets = {
        "none": [],
        "client": client_tools,
        "server": server_tools,
        "all": server_tools + client_tools,  # legacy order: server first, no dedup
        # A real node's built-in command set (captured from the dev node container).
        "node_dev": server_tools + corpus["node_dev"]["client_tools"],
    }
    personas = {"default": DEFAULT_PERSONA, "custom": "Speak like a dry British butler. Never use emoji.", "empty": ""}
    agents = {"none": None, **corpus["agents"]}

    cases = []

    def add(name, **kw):
        cases.append({"name": name, **kw})

    # Axes from docs/cc/03 §3.4 / §9 G1. Not a full cross product: one base case per axis value.
    base = dict(tools="all", flags=True, persona="default", date_keys=True, agents="none",
                hierarchy=False, characterization=None, thinking=False, room="kitchen", voice_mode="brief")
    add("base", **base)
    for t in tool_sets:
        add(f"tools_{t}", **{**base, "tools": t})
    add("no_flags", **{**base, "flags": False})
    add("real_node", **{**base, "tools": "node_dev", "flags": "node_dev"})
    add("bare", **{**base, "tools": "none", "flags": False, "date_keys": False, "persona": "empty"})
    for p in personas:
        add(f"persona_{p}", **{**base, "persona": p})
    add("no_date_keys", **{**base, "date_keys": False})
    for a in corpus["agents"]:
        add(f"agents_{a}", **{**base, "agents": a})
    add("agents_big_hierarchy", **{**base, "agents": "big", "hierarchy": True})
    add("agents_big_no_room", **{**base, "agents": "big", "hierarchy": True, "room": ""})
    add("characterization_on", **{**base, "characterization": "Alex likes short answers and dry humor."})
    add("characterization_on_empty", **{**base, "characterization": ""})
    add("thinking_on", **{**base, "thinking": True})
    add("verbose_office", **{**base, "room": "office", "voice_mode": "verbose"})

    date_keys = sorted(corpus.get("date_keys") or [
        "today", "tomorrow", "yesterday", "this_weekend", "next_weekend", "last_weekend",
        "tonight", "this_morning", "tomorrow_morning", "next_week",
    ])

    out = args.out
    out.mkdir(parents=True, exist_ok=True)
    index = []
    for P in providers:
        for c in cases:
            prov = P()
            prov.include_thinking = c["thinking"]
            nc = {"room": c["room"], "voice_mode": c["voice_mode"], "household_persona": personas[c["persona"]]}
            if c["date_keys"]:
                nc["date_keys"] = list(date_keys)
            if c["agents"] != "none":
                nc["agents"] = copy.deepcopy(agents[c["agents"]])
            if c["hierarchy"]:
                nc["room_hierarchy"] = copy.deepcopy(corpus["room_hierarchy"])
            if c["characterization"] is not None:
                nc["characterization"] = c["characterization"]
            tools = copy.deepcopy(tool_sets[c["tools"]])
            if c["flags"] == "node_dev":
                flags = copy.deepcopy(corpus["node_dev"]["command_flags"])
            else:
                flags = copy.deepcopy(corpus["command_flags"]) if c["flags"] else []
            inputs = {"node_context": copy.deepcopy(nc), "tools": c["tools"], "command_flags": flags,
                      "characterization_injection": c["characterization"] is not None,
                      "include_thinking": c["thinking"]}
            system = wrap(ConversationHandler, prov, nc, tools, flags, c["characterization"] is not None)
            fixture = {
                "provider": prov.name,
                "case": c["name"],
                "inputs": inputs,
                "system_prompt": system,
                "user_message_suffix": prov.user_message_suffix,
                "supports_native_tools": prov.supports_native_tools,
                "response_format": prov.get_response_format(),
            }
            if prov.supports_native_tools:
                fixture["native_tools"] = prov.build_tools(copy.deepcopy(tool_sets[c["tools"]]))
            name = f"{prov.name}__{c['name']}.json"
            (out / name).write_text(json.dumps(fixture, indent=1, ensure_ascii=False) + "\n")
            index.append({"file": name, "sha256": hashlib.sha256(system.encode()).hexdigest()})

    # Inputs shared by every case, so Go tests can load them once.
    (out / "_inputs.json").write_text(json.dumps(
        {"tool_sets": tool_sets, "personas": personas, "date_keys": date_keys},
        indent=1, ensure_ascii=False) + "\n")

    # Per-turn blocks (docs/cc/03 §3.3 "Per-turn messages").
    blocks = {
        "speaker_name_only": core_rules.build_speaker_block("Alex"),
        "speaker_with_memories": core_rules.build_speaker_block("Alex", "- [preference] Likes oat milk\n- [fact] Dog is named Leo"),
        "speaker_memories_only": core_rules.build_speaker_block("", "- [fact] Dog is named Leo"),
        "speaker_unknown": core_rules.build_speaker_block(""),
        "ambient": core_rules.build_ambient_context_block("Time: 7:45 PM\nWeather: 52F cloudy"),
        "ambient_empty": core_rules.build_ambient_context_block(""),
        "personality_reminder_default": core_rules.build_personality_reminder(DEFAULT_PERSONA),
        "personality_reminder_empty": core_rules.build_personality_reminder(""),
        "characterization": core_rules.build_characterization_section("Alex likes short answers."),
        "characterization_empty": core_rules.build_characterization_section(""),
    }
    constants = {k: getattr(core_rules, k) for k in dir(core_rules) if k.isupper() and isinstance(getattr(core_rules, k), str)}
    (out / "_blocks.json").write_text(json.dumps({"blocks": blocks, "constants": constants}, indent=1, ensure_ascii=False) + "\n")

    # G5: parse_response / sanitize_text per provider.
    raws = json.loads((HERE / "inputs" / "parse_corpus.json").read_text())
    parse = []
    for P in providers:
        prov = P()
        for r in raws:
            parse.append({"provider": prov.name, "raw": r,
                          "parse_response": prov.parse_response(r),
                          "sanitize_text": prov.sanitize_text(r)})
    (out / "_parse.json").write_text(json.dumps(parse, indent=1, ensure_ascii=False) + "\n")

    (out / "_index.json").write_text(json.dumps(index, indent=1) + "\n")
    print(f"wrote {len(index)} prompt fixtures, {len(blocks)} blocks, {len(parse)} parse cases to {out}")


if __name__ == "__main__":
    main()
