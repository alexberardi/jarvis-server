#!/usr/bin/env python3
"""Re-baseline the prompt goldens from the Go builder after a deliberate prompt change.

    JARVIS_GOLDEN_DUMP=/tmp/g.jsonl go test -run 'TestG1PromptsGolden|TestPerTurnBlocksGolden' ./internal/modules/cc/prompts/
    tools/golden/rebaseline.py /tmp/g.jsonl

Rewrites only each fixture's "system_prompt" (and "native_tools" for native-mode fixtures) and _blocks.json's blocks, keeping the files'
format (json.dump indent=1, non-ASCII as is). Review the diff before committing.
"""
import json
import os
import sys

DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "fixtures", "golden", "prompts")
rows = [json.loads(line) for line in open(sys.argv[1])]
files, blocks = {}, {}
for r in rows:
    if r["file"] == "_blocks.json":
        blocks[r["block"]] = r["value"]
    else:
        files.setdefault(r["file"], {}).update(
            {k: (json.loads(v) if k == "native_tools" else v) for k, v in r.items() if k != "file"})
changed = 0
for name, fields in files.items():
    path = os.path.join(DIR, name)
    d = json.load(open(path))
    new = {k: v for k, v in fields.items() if d.get(k) != v}
    if new:
        d.update(new)
        with open(path, "w") as f:
            json.dump(d, f, indent=1, ensure_ascii=False)
            f.write("\n")
        changed += 1
if blocks:
    path = os.path.join(DIR, "_blocks.json")
    d = json.load(open(path))
    for k, v in blocks.items():
        if d["blocks"].get(k) != v:
            d["blocks"][k] = v
            changed += 1
    with open(path, "w") as f:
        json.dump(d, f, indent=1, ensure_ascii=False)
        f.write("\n")
print(f"{changed} fixture(s) re-baselined from {len(rows)} rendered prompt(s)")
