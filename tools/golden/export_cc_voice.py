"""Export the voice-pipeline text fixtures (docs/cc/01 §9, 02 §9) from the legacy command-center.

    TZ=UTC ../jarvis-command-center/.venv/bin/python tools/golden/export_cc_voice.py \
        --cc ../jarvis-command-center --out fixtures/golden/voice

Writes (all from the REAL Python functions; the Python repos are frozen, so this lives here):

- shapes.json    every transcript_filter shape detector over a transcript corpus, plus
                 addressed_household_member and response_claims_action rows
- hints.json     build_direction_hint, build_affect_hint, build_turn_hint,
                 should_double_check_sentinel, build_profile_match_hint over input grids
- text.json      clean_for_tts, exchange_complete.apply_to_result, _rewrite_terminal_filler,
                 _is_transient_system_block, extract_sentences / sentence split / chunk groups,
                 ThinkBlockStripper ops
- wake.json      wake_phrase_present / wake_phrase_similarity, slice_leading_wav lengths
- ack.json       generate_acknowledgment's matched pool (legacy substring match) per text, and
                 the M5 word-boundary pool (computed here with the same pools)
- engine.json    _canonical_args_key, collect_tool_keywords / utterance_matches_keywords,
                 normalize_param_type / find_invalid_params, and every nag string (evaluated
                 from the f-string nodes of the real engine source with bound variables)
- format.json    _format_tool_result_text_mode (the LLM user message it sends and the result,
                 with a stub LLM reply) and stream_continue_with_tool_results (the LLM messages,
                 the spoken sentences and the committed message, with stub LLM deltas and TTS)
"""

import argparse
import ast
import asyncio
import copy
import hashlib
import inspect
import io
import json
import os
import re
import sys
import textwrap
import types
import wave
from pathlib import Path

TRANSCRIPTS = [
    "", "   ", "*sniff*", "*sad noises.*", "[laughter]", "(coughing)", "<inaudible>",
    "*sniff* *cough*", "[Music] [Applause].", "...", "--", "???", "!!!", "…", "-", " - ",
    "open the *kitchen* light", "turn on the living room lights", "Turn off the lights.",
    "please turn off the lights", "Jarvis, turn on the lamp", "hey jarvis turn the fan on",
    "can you turn on the lights", "turn on the lights?", "switch", "stop music", "stop",
    "pause", "skip", "next song", "play some jazz", "turn it down", "turn up the volume",
    "volume up", "louder", "go back", "previous", "crank it up", "turn down the music",
    "- Uh-huh. - Eat it.", "- Yeah.", "Uh-huh", "Eat it.", "Oh.", "Okay.", "Wow.",
    "Thank you.", "Okay thanks", "what's the weather", "What should I do with Miles today?",
    "who is Leo?", "who is leo", "Leo took his medicine", "I took my pills",
    "she gave the dog its meds", "I gave Leo his medicine", "his medicine.",
    "I took a walk", "set a timer for 5 minutes", "remind me to call mom",
    "add milk to the shopping list", "log my weight", "cancel the alarm",
    "Should I bring an umbrella", "is it going to rain", "don't do that",
    "already done. Wow. Miles, come here.", "Miles come here", "come here, Miles",
    "come here Miles", "Miles said he wants pizza", "Jess, can you grab that?",
    "hey Jess", "Jarvis, what time is it", "Café au lait, s'il vous plaît",
    "naïve résumé", "Привет, как дела?", "日本語のテキスト", "Ünïcödé lights on",
    "turn on the café lights", "   turn   on   the   lights   ", "turn on the\nlights",
    "the dog got his treat", "Grandma finally took her pills", "I've got this",
    "what's 5*3", "user_id is 4", "STOP", "Please, stop the music",
]

REPLIES = [
    "", "I'll check on Leo's meds for you.", "Let me look that up.",
    "I'm going to set a timer.", "I've marked it as taken.", "Done — I added that.",
    "I just logged it.", "Leo's medicine is marked as taken.", "That's been scheduled.",
    "I'm marking that now.", "Setting a reminder for 8.", "Let me know if you need anything.",
    "I'll keep that in mind.", "The weather is sunny.", "Sure thing!", "I can play that.",
    "It was checked yesterday.", "Turning on the lights now.",
]

MEMBER_SETS = [
    [], ["Miles"], ["Miles", "Jess B."], ["Jarvis", "Hey", "Al"], ["A", "Bo"],
    ["miles", "Miles"], ["Zoë"], [None, 5, "Leo"],
]

TTS_TEXTS = [
    "", "Hello there.", "**Bold** and *italic* and ***both***.", "Use `code` here.",
    "```python\nprint('x')\n```\nDone.", "![img](http://x/y.png) see [link](http://a.b)",
    "__under__ and _ital_ but user_id stays", "~~strike~~ text", "# Heading\n## Sub\ntext",
    "> quote\n>more", "- item one\n* item two\n+ item three", "1. first\n2. second",
    "---\ntext\n***\n___", "Sunny ☀️ today 😄!", "Flag 🇺🇸 and keycap 1️⃣", "5*3 equals 15",
    "stray * star", "orphan *hello", "trailing*", "Timer set! <exchange_complete/>",
    "<Exchange-Complete /> Bye", "a  b\t\tc", "x\n\n\n\ny", "  spaced  ", "Price: $5 €3 ±2",
    "multi\n\n\n\nlines **with** bold", "👨‍👩‍👧 family", "⏰ alarm ✓ done ⭐",
    "Text with — dash and … ellipsis", "**unclosed bold", "`unclosed code",
    "line one\n# not heading in middle", "Ünïcode **bold** ok",
]

SENTENCE_TEXTS = [
    "", "One sentence.", "One. Two.", "One. Two. Three.", "A! B? C. D. E. F. G. H. I.",
    "No punctuation at all", "Ends with space. ", "Dr. Smith is here. He said hi!",
    "Line one.\nLine two.\n\nLine three.", "Wait...  what?  Really!", "a.b.c. d",
    "Sentence one.   Sentence two!\tSentence three?\nSentence four. Five.",
]

THINK_TEXTS = [
    "", "plain", "<think>reason</think> answer", "<think>a</think>\n\nb<think>c</think>  d",
    "before <think>open", "<think>x</think>", "no close </think> here", "<THINK>x</THINK> y",
    "<think>multi\nline</think>\nresult.",
]

WAKE_ROWS = [
    ("jarvis", "jarvis"), ("Hey Jarvis.", "jarvis"), ("travis", "jarvis"),
    ("jervis", "jarvis"), ("service", "jarvis"), ("nervous", "jarvis"), ("", "jarvis"),
    ("hello there", "jarvis"), ("Hey, Jarvis!", " Jarvis "), ("anything", ""),
    ("jarviss", "jarvis"), ("ajrvis", "jarvis"), ("j", "jarvis"), ("JARVIS what", "jarvis"),
    ("hey computer", "computer"), ("compooter", "computer"), ("- That's it.", "jarvis"),
    ("No, it's younger.", "jarvis"), ("jar vis", "jarvis"), ("don't", "jarvis"),
    ("Járvis", "jarvis"), ("arvis jarv", "jarvis"),
]

ACK_TEXTS = [
    "what's the weather", "will it rain", "train schedule", "show me the news", "how are you",
    "set a timer", "turn on the light", "lights please", "play music", "display the screen",
    "search for cats", "find my phone", "cook dinner", "breaking headline", "game score",
    "sports", "hello", "", "Somehow it works", "whatever", "lamp", "nowhere", "anyhow",
    "recipe ideas", "speakers", "reminder", "remind me", "turn offer",
]


def wav_bytes(seconds, rate=16000, channels=1, width=2):
    n = int(seconds * rate)
    out = io.BytesIO()
    with wave.open(out, "wb") as w:
        w.setnchannels(channels)
        w.setsampwidth(width)
        w.setframerate(rate)
        w.writeframes(bytes((i * 7) % 256 for i in range(n * channels * width)))
    return out.getvalue()


def eval_fstrings(func, prefix, bindings):
    """Evaluate every string literal / f-string in func's source that starts with prefix."""
    src = textwrap.dedent(inspect.getsource(func))
    tree = ast.parse(src)
    out = []
    for node in ast.walk(tree):
        if isinstance(node, (ast.JoinedStr, ast.Constant)):
            if isinstance(node, ast.Constant) and not isinstance(node.value, str):
                continue
            try:
                val = eval(compile(ast.Expression(node), "<nag>", "eval"), dict(bindings))
            except Exception:
                continue
            if isinstance(val, str) and val.startswith(prefix):
                out.append(val)
    return out


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--cc", required=True, type=Path)
    ap.add_argument("--out", required=True, type=Path)
    args = ap.parse_args()
    sys.path.insert(0, str(args.cc.resolve()))
    os.environ.setdefault("LOG_FULL_SYSTEM_PROMPT", "false")

    from app.core import transcript_filter as tf
    from app.core import direction_hint, affect_hint, turn_context, profile_match
    from app.core import tts_text, exchange_complete, param_validation, tool_routing
    from app.core import streaming_handler, wake_verification
    from app.core import conversation_handler as ch
    from app.core.conversation_handler import ConversationHandler
    from app.core import tool_execution_engine as tee
    from app.core.utils.think_block_stripper import ThinkBlockStripper
    from app.core.prompt_providers.large.untrained.qwen3_14b_compressed import Qwen3_14B_Compressed
    from app.core.prompt_providers.medium.untrained.qwen3_5_9b_compressed import Qwen3_5_9B_Compressed
    from app.services import acknowledgment_service as ack

    out = args.out
    out.mkdir(parents=True, exist_ok=True)

    def dump(name, data):
        # One row per line: compact, diffable.
        lines = ["{"]
        keys = list(data)
        for ki, k in enumerate(keys):
            lines.append(json.dumps(k) + ": [")
            rows = data[k]
            for ri, r in enumerate(rows):
                lines.append(json.dumps(r, ensure_ascii=False, separators=(",", ":")) + ("," if ri < len(rows) - 1 else ""))
            lines.append("]" + ("," if ki < len(keys) - 1 else ""))
        lines.append("}")
        (out / name).write_text("\n".join(lines) + "\n")
        n = len(data) if isinstance(data, list) else sum(len(v) for v in data.values() if isinstance(v, list))
        print(f"{name}: {n} rows")

    # --- shapes ---
    shapes = []
    for t in TRANSCRIPTS:
        shapes.append({
            "text": t,
            "is_stt_noise": tf.is_stt_noise(t),
            "is_device_command_shaped": tf.is_device_command_shaped(t),
            "is_music_control_shaped": tf.is_music_control_shaped(t),
            "has_multi_speaker_markers": tf.has_multi_speaker_markers(t),
            "is_short_non_command_fragment": tf.is_short_non_command_fragment(t),
            "is_action_command_shaped": tf.is_action_command_shaped(t),
            "is_report_shaped": tf.is_report_shaped(t),
            "is_question_shaped": tf.is_question_shaped(t),
            "response_claims_action": tf.response_claims_action(t),
        })
    for r in REPLIES:
        shapes.append({"text": r, "response_claims_action": tf.response_claims_action(r)})
    addressed = []
    for t in TRANSCRIPTS:
        for names in MEMBER_SETS:
            addressed.append({"text": t, "names": names, "member": tf.addressed_household_member(t, names)})
    dump("shapes.json", {"shapes": shapes, "addressed": addressed})

    # --- hints ---
    direction = []
    hint_table = []
    hint_index = {}

    def hint_id(h):
        """Rows reference hint strings by index into "hint_strings" (null stays null)."""
        if h is None:
            return None
        if h not in hint_index:
            hint_index[h] = len(hint_table)
            hint_table.append(h)
        return hint_index[h]

    pre_vals = [None, 0.32, 1.5, 3.25, 4.51, 0.05]
    conf_vals = [None, 0.5, 0.75, 0.97]
    sources = [None, "wake", "follow_up", "chat"]
    dir_transcripts = [None, "", "turn on the lights", "pause", "- Yeah. - Eat it.", "Oh.",
                       "what's the weather like today"]
    for pre in pre_vals:
        for conf in conf_vals:
            for src in sources:
                for t in dir_transcripts:
                    for known in (None, True):
                        for sp, kind in ((None, None), (True, None), (True, "podcast")):
                            kw = dict(pre_wake_speech_seconds=pre, wake_confidence=conf, turn_source=src,
                                      transcript=t, speaker_known=known, self_playback=sp,
                                      self_playback_kind=kind)
                            direction.append({**kw, "hint": hint_id(direction_hint.build_direction_hint(**kw))})
    affect = []
    for a in [None, "x", {}, {"confidence": 0.9, "arousal": "low", "read": "tired"},
              {"confidence": 0.9, "arousal": "high", "read": " excited "},
              {"confidence": 0.49, "arousal": "low", "read": "tired"},
              {"confidence": "0.8", "arousal": "high", "read": "upbeat"},
              {"confidence": "bad", "arousal": "low", "read": "tired"},
              {"confidence": 0.9, "arousal": "neutral", "read": "calm"},
              {"confidence": 0.9, "arousal": "low", "read": "  "},
              {"confidence": 0.9, "arousal": "low", "read": 5},
              {"confidence": True, "arousal": "low", "read": "sleepy"},
              {"arousal": "low", "read": "tired"}, {"confidence": None, "arousal": "low", "read": "x"}]:
        affect.append({"affect": a, "hint": affect_hint.build_affect_hint(a)})
    turn = []
    turn_transcripts = [None, "turn on the lights", "Okay.", "already done. Wow. Miles, come here.",
                        "pause", "Leo took his medicine"]
    for src in [None, "wake", "follow_up", "chat", "bogus"]:
        for conf in [None, 0.5, 0.75, 0.97]:
            for it in [None, 0, 2, 3, -1]:
                for pre in [None, 0.2]:
                    for verified in [None, True, False]:
                        for t in turn_transcripts:
                            for sp in [None, True]:
                                for verdict, dr, dmax in [(None, None, None), ("verified", 2, 2),
                                                          ("unverified", 0, 2), ("unverified", 2, 2),
                                                          ("unverified", 3, None), ("clip_unreliable", 5, 2)]:
                                    if src not in ("follow_up",) and verdict not in (None,):
                                        continue
                                    if src == "follow_up" and conf is not None:
                                        continue
                                    if src != "follow_up" and it not in (None,):
                                        continue
                                    for names in ([], ["Miles", "Jess"]):
                                        if names and src != "follow_up":
                                            continue
                                        kw = dict(turn_source=src, wake_confidence=conf, follow_up_iteration=it,
                                                  pre_wake_speech_seconds=pre, wake_verified=verified,
                                                  transcript=t, self_playback=sp, self_playback_kind=None,
                                                  conversation_wake_verdict=verdict, doubt_round=dr,
                                                  doubt_max_rounds=dmax, member_names=names)
                                        row = {**kw, "hint": hint_id(turn_context.build_turn_hint(**kw))}
                                        row["double_check"] = turn_context.should_double_check_sentinel(
                                            src, conf, it, pre, verified, t)
                                        turn.append(row)
    profile = []
    blocks = [None, "", "You are speaking with Alex.",
              "You are speaking with Alex.\n\nUser Profile — facts:\n- [fact] Has golden doodle dogs named Leo and Groot who are brothers\n- [preference] Likes oat milk\n- [fact] Administers Keppra to Leo\n- [fact] Leo is 4\n- [fact] Kaitlyn is his sister",
              "- one leo\r\n- two leo\r- three leo\x0b- four leo - five leo"]
    utts = [None, "", "Who is Leo?", "who is leo", "I gave Leo his medication", "tell me about Kaitlyn",
            "what milk do I like", "the and who", "LEO", "is groot a dog", "Leo.", "  Leo?  ",
            "remind me about leo", "Café oat milk"]
    for b in blocks:
        for u in utts:
            profile.append({"utterance": u, "block": b, "hint": profile_match.build_profile_match_hint(u, b)})
    # None-valued keys are omitted (absent = Python None) to keep the file small.
    direction = [{k: v for k, v in r.items() if v is not None} for r in direction]
    turn = [{k: v for k, v in r.items() if v is not None and v != []} for r in turn]
    dump("hints.json", {"hint_strings": hint_table, "direction": direction, "affect": affect, "turn": turn,
                        "profile": profile})

    # --- text ---
    clean = [{"text": t, "out": tts_text.clean_for_tts(t)} for t in TTS_TEXTS]
    apply_rows = []
    for sr in ["complete", "not_for_me", "tool_calls"]:
        for m in [None, "", "Timer set! <exchange_complete/>", "Bye <EXCHANGE_COMPLETE>", "no marker",
                  "  <exchange complete/>  "]:
            res = exchange_complete.apply_to_result({"stop_reason": sr, "assistant_message": m})
            apply_rows.append({"stop_reason": sr, "message": m, "out": res.get("assistant_message"),
                               "end_of_exchange": bool(res.get("end_of_exchange"))})
    filler = []
    for sr in ["complete", "not_for_me", "tool_calls", "validation_required", "error", "server_tool_complete", None]:
        for m in [None, "Task completed.", "task complete!", "  Completed . ", "Done.", "All tasks completed!!",
                  "Task is complete", "Task finished...", "Okay", "TASK DONE"]:
            res = ConversationHandler._rewrite_terminal_filler({"stop_reason": sr, "assistant_message": m})
            filler.append({"stop_reason": sr, "message": m, "out": res.get("assistant_message")})
    transient = []
    for role, c in [("system", "You are speaking with Alex."), ("system", "User Profile - If user asks x"),
                    ("system", "User Profile — these facts"), ("system", "Router hint: x"),
                    ("system", "Respond naturally in plain text. Do not"), ("system", "RECENTLY SHOWN (act"),
                    ("system", "<ambient_context>\nx"), ("system", "You are Jarvis"), ("user", "You are speaking with x"),
                    ("system", " You are speaking with ")]:
        transient.append({"role": role, "content": c,
                          "transient": ch._is_transient_system_block({"role": role, "content": c})})
    sentences = []
    for t in SENTENCE_TEXTS:
        sents = streaming_handler.extract_sentences(t)
        groups = sents if len(sents) <= 2 else streaming_handler._group_into_chunks(sents, streaming_handler._SENTENCES_PER_CHUNK)
        sentences.append({"text": t, "split": re.split(r"(?<=[.!?])\s+", t), "sentences": sents, "groups": groups})
    think = []
    ts = ThinkBlockStripper("<think>", "</think>")
    for t in THINK_TEXTS:
        think.append({"text": t, "strip_complete": ts.strip_complete_blocks(t), "has_open": ts.has_open_block(t),
                      "strip_all": ts.strip_all(t)})
    dump("text.json", {"clean_for_tts": clean, "apply_exchange_complete": apply_rows, "filler": filler,
                       "transient": transient, "sentences": sentences, "think": think})

    # --- wake ---
    wake = []
    for t, p in WAKE_ROWS:
        wake.append({"transcript": t, "phrase": p, "present": wake_verification.wake_phrase_present(t, p),
                     "similarity": wake_verification.wake_phrase_similarity(t, p)})
    slices = []
    for secs, rate, ch_, width in [(3.0, 16000, 1, 2), (1.0, 16000, 1, 2), (3.0, 22050, 2, 2), (2.2, 8000, 1, 1)]:
        src = wav_bytes(secs, rate, ch_, width)
        sl = wake_verification.slice_leading_wav(src)
        slices.append({"seconds": secs, "rate": rate, "channels": ch_, "width": width,
                       "in_len": len(src), "out_len": len(sl) if sl else None,
                       "out_sha256": hashlib.sha256(sl).hexdigest() if sl else None})
    dump("wake.json", {"wake": wake, "slice": slices})

    # --- ack ---
    m5 = [(re.compile(r"\b(?:" + p + r")\b", re.IGNORECASE), pool) for p, pool in ack._KEYWORD_POOLS.items()]
    rows = []
    for t in ACK_TEXTS:
        legacy = next((pool for pat, pool in ack._COMPILED_POOLS if pat.search(t)), ack._GENERIC_POOL)
        bounded = next((pool for pat, pool in m5 if pat.search(t)), ack._GENERIC_POOL)
        picks = set()
        for _ in range(200):
            picks.add(ack.generate_acknowledgment(t))
        rows.append({"text": t, "legacy_pool": legacy, "m5_pool": bounded, "legacy_picks": sorted(picks)})
    dump("ack.json", {"ack": rows, "generic": [ack._GENERIC_POOL]})

    # --- engine ---
    canon = []
    for a in ['{"b": 1, "a": 2}', '{"a":2,"b":1}', "null", "{}", "[1, 2]", "not json", '"str"',
              '{"x": "é", "y": [1.5, 1e20, true, null], "z": {"d": 1, "c": 2}}', '{"a": 1, "a": 2}',
              '{"emoji": "😄"}', '{"n": 12345678901234567890123}', '{"f": 1.0, "g": -0.0}', "  {}  ", ""]:
        canon.append({"arguments": a, "key": tee._canonical_args_key(a)})
    kw_sources = [
        [{"command_name": "medication", "keywords": ["took my", "medicine", "Med", "  ", 5, "medicine"]},
         {"command_name": "weather", "keywords": None}, "junk",
         {"type": "function", "function": {"name": "t", "keywords": ["Timer", "alarm"]}},
         {"keywords": "notalist"}, {"keywords": [], "function": {"keywords": ["ignored"]}}],
        [{"function": {"name": "x", "keywords": ["lights", "took my"]}}],
    ]
    pool = tool_routing.collect_tool_keywords(*kw_sources)
    kw_match = []
    for u in ["Leo took my pills", "goodnight", "turn on the lights", "", "...", "TIMER please",
              "med", "the medicine-cabinet", "took  my", "Took-My stuff", "alarms", "café timer"]:
        kw_match.append({"utterance": u, "pool": pool, "match": tool_routing.utterance_matches_keywords(u, pool)})
    for u, kws in [("good night", ["night"]), ("goodnight", ["night"]), ("a bc", ["bc", "a"]),
                   ("x", []), ("set an alarm", ["ALARM!"])]:
        kw_match.append({"utterance": u, "pool": kws, "match": tool_routing.utterance_matches_keywords(u, kws)})
    norm = []
    for t in ["string", " Integer ", "array<string>", "ARRAY[datetime]", "string[]", "", "  ", "array<>",
              "array[]", "[]", "array< int >", None]:
        b, arr = param_validation.normalize_param_type(t)
        norm.append({"type": t, "base": b, "is_array": arr})
    commands = [
        {"command_name": "set_timer", "parameters": [
            {"name": "duration", "type": "int"}, {"name": "label", "type": "string"},
            {"name": "unit", "type": "string", "enum_values": ["seconds", "minutes"]},
            {"name": "when", "type": "datetime"}, {"name": "day", "type": "date"},
            {"name": "ratio", "type": "float"}, {"name": "on", "type": "bool"},
            {"name": "times", "type": "array<datetime>"}, {"name": "tags", "type": "string[]"},
            {"name": "x", "type": "weird"}, {"name": "flag", "type": "string", "enum_values": ["True", "False"]},
            {"name": "", "type": "int"}, {"name": "notype"}, None]},
        {"command_name": "set_timer", "parameters": [{"name": "label", "type": "int"}]},
        {"parameters": [{"name": "a", "type": "int"}]},
        {"command_name": "other", "parameters": None},
    ]
    calls_list = [
        [{"function": {"name": "set_timer", "arguments": '{"duration": 5, "label": "tea", "unit": "minutes"}'}}],
        [{"function": {"name": "set_timer", "arguments": '{"duration": "5", "label": 3, "unit": "hours", "when": "2026-10-06T10:00:00", "day": "2026-10-06", "ratio": true, "on": 1, "times": ["2026-10-06T10:00:00Z", "today"], "tags": "x", "x": {}, "flag": true}'}}],
        [{"function": {"name": "set_timer", "arguments": '{"duration": 5.0, "ratio": 2, "when": "2026-10-06T10:00:00+02:00", "day": "2026-1-6", "times": [], "unit": null, "flag": "True"}'}}],
        [{"function": {"name": "set_timer", "arguments": "not json"}}, {"function": {"name": "set_timer", "arguments": "[1]"}},
         {"function": {"name": "", "arguments": "{}"}}, {"function": {"name": "unknown", "arguments": '{"a": "b"}'}}],
        [{"function": {"name": "set_timer", "arguments": '{"duration": true, "on": false, "day": "２０２６-10-06"}'}}],
    ]
    invalid = []
    for calls in calls_list:
        type_map, enum_map = {}, {}
        for cmd in commands:
            cmd_name = cmd.get("command_name")
            if not cmd_name:
                continue
            for param in cmd.get("parameters") or []:
                name = (param or {}).get("name")
                ptype = (param or {}).get("type")
                if name and ptype:
                    type_map.setdefault(cmd_name, {})[name] = ptype
                enum_vals = (param or {}).get("enum_values")
                if name and enum_vals and isinstance(enum_vals, list):
                    enum_map.setdefault(cmd_name, {})[name] = enum_vals
        invalid.append({"calls": [{"name": c["function"]["name"], "arguments": c["function"]["arguments"]} for c in calls],
                        "invalid": param_validation.find_invalid_params(calls, type_map, enum_map)})
    execute = tee.ToolExecutionEngine.execute
    nags = {
        "must_call_1": eval_fstrings(execute, "[MUST_CALL_RETRY] You MUST", {"retry_count": 0}),
        "must_call_2": eval_fstrings(execute, "[MUST_CALL_RETRY] You MUST", {"retry_count": 1}),
        "iso": eval_fstrings(execute, "[ISO_DATE_RETRY]", {}),
        "dedupe": eval_fstrings(execute, "[TOOL_DEDUPE]", {"_TOOL_DEDUPE_TAG": tee._TOOL_DEDUPE_TAG, "dup_name": "get_weather"}),
        "invalid": eval_fstrings(execute, "[INVALID_PARAM_RETRY", {"retry_count": 0, "max_retries": 2,
                                                                   "invalid_params": ["a.b expected int", "c.d must be one of: x, y"]}),
        "double_check": eval_fstrings(execute, "[NOT_FOR_ME_DOUBLE_CHECK]", {"_DC_TAG": "[NOT_FOR_ME_DOUBLE_CHECK]"}),
    }
    dump("engine.json", {"canonical": canon, "keywords": [{"sources": kw_sources, "pool": pool}],
                         "keyword_match": kw_match, "normalize_param_type": norm,
                         "param_commands": [{"commands": commands}], "invalid_params": invalid,
                         "nags": [nags]})

    # --- format ---
    outputs_cases = [
        ("all_messages", [{"success": True, "message": "Timer set for 5 minutes."}, '{"response": "It is sunny."}']),
        ("one_missing", [{"success": True, "message": "Done."}, {"temperature": 72, "conditions": "sunny ☀️"}]),
        ("knowledge", [{"query": "why is the sky blue"}]),
        ("knowledge_ctx", [{"context": {"question": "who won"}}]),
        ("knowledge_two", [{"query": "a"}, {"query": "b"}]),
        ("string_plain", ["not json at all"]),
        ("failure", [{"success": False, "error": "Node offline"}]),
        ("failure_no_error", [{"success": False}]),
        ("emoji_only_message", [{"message": "😄"}]),
        ("blank_message", [{"message": "   ", "response": "fallback text"}]),
        ("empty_message_response", [{"message": "", "response": "Use response."}]),
        ("list_output", [[1, 2, 3]]),
        ("null_output", [None]),
        ("markdown_message", [{"message": "**Done** — set *two* timers."}]),
        ("none", []),
    ]
    replies = [
        "It's sunny and 72.",
        "<think>let me see</think>It's sunny and 72. <exchange_complete/>",
        '<tool_call>{"name": "get_weather", "arguments": {}}</tool_call>',
        '{"name": "get_weather", "arguments": {}}',
        "**Sunny** today 😄",
        "",
    ]
    fmt_rows = []
    stream_rows = []

    class FakeLLM:
        def __init__(self, reply=None, deltas=None):
            self.reply, self.deltas, self.sent = reply, deltas, None

        async def chat_completion(self, messages, **kw):
            self.sent = copy.deepcopy(messages)
            self.kw = {k: v for k, v in kw.items() if k != "conversation_id"}
            return {"choices": [{"message": {"content": self.reply}}]}

        async def chat_completion_stream(self, messages, **kw):
            self.sent = copy.deepcopy(messages)
            self.kw = kw
            for d in self.deltas:
                yield {"delta": d}
            yield {"done": True}

    class FakeTTS:
        def __init__(self):
            self.spoken = []

        async def speak_stream(self, sentence, **kw):
            self.spoken.append(sentence)

            async def it():
                yield b"x"
            return it(), {}

    from app.core import conversation_cache as cc_mod
    cache = cc_mod.conversation_cache
    base_messages = [
        {"role": "system", "content": "SYS"},
        {"role": "user", "content": "  Is it going to rain today?\n/no_think  "},
        {"role": "assistant", "content": "", "tool_calls": [{"id": "call_1"}]},
    ]
    for prov_cls in (Qwen3_14B_Compressed, Qwen3_5_9B_Compressed):
        prov = prov_cls()
        for case, outs in outputs_cases:
            trs = [{"tool_call_id": f"call_{i}", "output": o} for i, o in enumerate(outs)]
            for reply in replies:
                if prov_cls is Qwen3_14B_Compressed:
                    llm = FakeLLM(reply=reply)
                    h = ConversationHandler(model=None, llm_client=llm, prompt_provider=prov)
                    msgs = copy.deepcopy(base_messages) + [
                        {"role": "tool", "tool_call_id": t["tool_call_id"],
                         "content": t["output"] if isinstance(t["output"], str) else json.dumps(t["output"])} for t in trs]
                    res = asyncio.run(h._format_tool_result_text_mode("conv-x", msgs, copy.deepcopy(trs)))
                    fmt_rows.append({"case": case, "outputs": outs, "reply": reply,
                                     "sent_user": llm.sent[-1]["content"] if llm.sent else None,
                                     "result": res, "messages_after": msgs})
                # streaming continue
                deltas = [reply[i:i + 7] for i in range(0, len(reply), 7)] if reply else []
                llm = FakeLLM(deltas=deltas)
                tts = FakeTTS()
                h = ConversationHandler(model=None, llm_client=llm, prompt_provider=prov)
                committed = {}
                cache.get_messages = lambda cid: copy.deepcopy(base_messages)
                cache.update_messages = lambda cid, m: committed.__setitem__("m", copy.deepcopy(m))
                cache.get_node_context = lambda cid: {}
                cache.set_referenced_items = lambda cid, items: None

                async def run():
                    gen = await h.stream_continue_with_tool_results("conv-x", copy.deepcopy(trs), tts)
                    if gen is None:
                        return None
                    async for _ in gen:
                        pass
                    return True

                ran = asyncio.run(run())
                stream_rows.append({
                    "provider": prov_cls.__name__, "case": case, "outputs": outs, "reply": reply,
                    "deltas": deltas, "fallback": ran is None,
                    "llm_messages": llm.sent[len(base_messages):] if llm.sent else None,
                    "spoken": tts.spoken, "committed": committed.get("m"),
                })
    dump("format.json", {"text_mode": fmt_rows, "continue_stream": stream_rows})


if __name__ == "__main__":
    main()
