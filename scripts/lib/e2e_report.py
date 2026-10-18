#!/usr/bin/env python3
"""Merges each suite's results.json in an scripts/e2e run into report.json and report.md.

A suite writes <out>/<suite>/results.json ({"suite", "status", "duration_ms", "tests": [...]},
one record per test, e2e/web/reporter.ts and tools/e2emobile) and scripts/e2e writes <out>/<suite>.exit. The
report.json schema is in docs/dev/e2e.md; loop item #149 reads it, so keep it stable (add
fields, don't rename them). Exits 1 when a test failed or a suite did not run to the end.
"""
import argparse
import datetime
import json
import os
import subprocess
import sys

FAILED = {"failed", "timedOut", "interrupted"}
KNOWN = ("web", "mobile")


def git(*args: str) -> str:
    try:
        return subprocess.run(["git", *args], capture_output=True, text=True, check=True).stdout.strip()
    except (OSError, subprocess.CalledProcessError):
        return ""


def load_suite(out: str, name: str) -> tuple[dict, list[dict]]:
    path = os.path.join(out, name, "results.json")
    exit_code = None
    try:
        with open(os.path.join(out, f"{name}.exit"), encoding="utf-8") as f:
            exit_code = int(f.read().strip() or 0)
    except (OSError, ValueError):
        pass
    try:
        with open(path, encoding="utf-8") as f:
            res = json.load(f)
    except (OSError, ValueError) as e:
        why = f"no results: {e}"
        try:
            with open(os.path.join(out, name, "error.txt"), encoding="utf-8") as f:
                why = f.read().strip()
        except OSError:
            pass
        return {"status": "error", "exit_code": exit_code, "error": why, "duration_ms": 0}, []
    tests = res.get("tests", [])
    status = "failed" if any(t["status"] in FAILED for t in tests) or res.get("status") != "passed" or exit_code else "passed"
    return {"status": status, "exit_code": exit_code, "error": None, "duration_ms": res.get("duration_ms", 0)}, tests


def counts(tests: list[dict]) -> dict:
    c = {"total": len(tests), "passed": 0, "failed": 0, "skipped": 0}
    for t in tests:
        if t["status"] == "passed":
            c["passed"] += 1
        elif t["status"] == "skipped":
            c["skipped"] += 1
        else:
            c["failed"] += 1
    return c


def markdown(rep: dict) -> str:
    c = rep["counts"]
    lines = [
        f"# E2E run {rep['run']}: {rep['status'].upper()}",
        "",
        f"- {c['passed']} passed, {c['failed']} failed, {c['skipped']} skipped of {c['total']}",
        f"- commit `{rep['git']['sha'][:12]}` on `{rep['git']['branch']}`; sandbox `{rep['sandbox']}`",
        f"- {rep['started_at']} → {rep['finished_at']}" + (f"; filter `{rep['filter']}`" if rep["filter"] else ""),
        "",
        "| suite | status | tests | time |",
        "|---|---|---|---|",
    ]
    for name, s in rep["suites"].items():
        n = sum(1 for t in rep["tests"] if t["suite"] == name)
        lines.append(f"| {name} | {s['status']} | {n} | {s.get('duration_ms', 0) / 1000:.1f} s |")
    failed = [t for t in rep["tests"] if t["status"] in FAILED]
    if failed:
        lines += ["", "## Failures", ""]
        for t in failed:
            lines += [f"### {t['suite']}: {t['title']}", "", f"`{t['file']}:{t['line']}`, {t['status']} after {t['duration_ms'] / 1000:.1f} s", ""]
            if t.get("error"):
                lines += ["```", t["error"].strip()[:4000], "```", ""]
            for s in t.get("screenshots", []):
                lines.append(f"![screenshot]({s})")
            if t.get("trace"):
                lines.append(f"- trace: `{t['trace']}` (`npx playwright show-trace` in e2e/web)")
            for a in t.get("attachments", []):
                lines.append(f"- attachment: `{a}`")
            for a in t.get("artifacts", [])[:30]:
                lines.append(f"- artifact: `{a}`")
            for e in t.get("console_errors", [])[:20]:
                lines.append(f"- console: `{e[:300]}`")
            lines.append("")
    for name, s in rep["suites"].items():
        if s.get("error"):
            lines += ["", f"**{name}**: {s['error']}"]
    lines += ["", "## Tests", "", "| status | suite | test | time | console errors |", "|---|---|---|---|---|"]
    for t in rep["tests"]:
        title = t["title"] + (f" (skipped: {t['skip_reason']})" if t.get("skip_reason") else "")
        lines.append(f"| {t['status']} | {t['suite']} | {title} | {t['duration_ms'] / 1000:.1f} s | {len(t.get('console_errors', []))} |")
    return "\n".join(lines) + "\n"


def main() -> int:
    p = argparse.ArgumentParser()
    p.add_argument("--out", required=True)
    p.add_argument("--run", required=True)
    p.add_argument("--sandbox", required=True)
    p.add_argument("--started", required=True)
    p.add_argument("--filter", default="")
    p.add_argument("--requested", default="all")
    p.add_argument("suites", nargs="*")
    a = p.parse_args()

    suites, tests = {}, []
    for name in a.suites:
        suites[name], t = load_suite(a.out, name)
        tests += t
    if a.requested == "all":
        for name in KNOWN:
            suites.setdefault(name, {"status": "not_run", "exit_code": None, "error": "not requested", "duration_ms": 0})
    bad = any(s["status"] in ("failed", "error") for s in suites.values()) or not tests
    rep = {
        "schema": 1,
        "run": a.run,
        "status": "failed" if bad else "passed",
        "started_at": a.started,
        "finished_at": datetime.datetime.now().astimezone().isoformat(timespec="seconds"),
        "sandbox": a.sandbox,
        "filter": a.filter or None,
        "git": {"sha": git("rev-parse", "HEAD"), "branch": git("rev-parse", "--abbrev-ref", "HEAD"),
                "dirty": bool(git("status", "--porcelain"))},
        "counts": counts(tests),
        "suites": suites,
        "tests": tests,
    }
    with open(os.path.join(a.out, "report.json"), "w", encoding="utf-8") as f:
        json.dump(rep, f, indent=2)
        f.write("\n")
    with open(os.path.join(a.out, "report.md"), "w", encoding="utf-8") as f:
        f.write(markdown(rep))
    c = rep["counts"]
    print(f"e2e: {rep['status']}: {c['passed']} passed, {c['failed']} failed, {c['skipped']} skipped; "
          f"{os.path.join(a.out, 'report.md')}", file=sys.stderr)
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
