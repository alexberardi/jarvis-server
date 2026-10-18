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
COVERAGE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "e2e", "coverage.json")


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


def kind(e: dict) -> str:
    return "covered" if e.get("tests") or e.get("flows") else "manual" if e.get("manual") else "pending" if e.get("pending") else "invalid"


def load_coverage(out: str, path: str = COVERAGE) -> dict | None:
    """The coverage gate's state (docs/decisions/2026-10-18-e2e-coverage-gate.md): per side, the
    entries of e2e/coverage.json by kind. The web side's routes are checked in CI
    (web/admin/src/coverage.test.tsx); the mobile side's screens by the mobile suite, whose
    <out>/mobile/coverage.json adds the missing, stale and invalid entries when it ran."""
    try:
        with open(path, encoding="utf-8") as f:
            cov = json.load(f)
    except (OSError, ValueError):
        return None
    res = {}
    for side in ("web", "mobile"):
        entries = cov.get(side, {})
        c = {"entries": len(entries), "covered": [], "manual": [], "pending": [], "invalid": [],
             "missing": None, "stale": None, "checked_by": "CI (web/admin/src/coverage.test.tsx)" if side == "web" else None}
        for name, e in sorted(entries.items()):
            k = kind(e)
            c[k].append(name if k in ("covered", "invalid") else f"{name}: {e[k]}")
        res[side] = c
    try:
        with open(os.path.join(out, "mobile", "coverage.json"), encoding="utf-8") as f:
            m = json.load(f)
        res["mobile"].update({"entries": m["screens"], "covered": m["covered"], "manual": m["manual"], "pending": m["pending"],
                              "invalid": m["invalid"], "missing": m["missing"], "stale": m["stale"], "checked_by": "this run's mobile suite"})
    except (OSError, ValueError, KeyError):
        pass
    return res


def coverage_md(cov: dict) -> list[str]:
    lines = ["", "## Coverage", "", "Every route and screen has tests, or a reason it has none (`e2e/coverage.json`).", "",
             "| | routes/screens | tests | manual | pending | missing | stale or invalid | checked by |", "|---|---|---|---|---|---|---|---|"]
    for side, c in cov.items():
        missing = ("in CI" if side == "web" else "?") if c["missing"] is None else str(len(c["missing"]))
        bad = len(c["invalid"]) + len(c["stale"] or [])
        lines.append(f"| {side} | {c['entries']} | {len(c['covered'])} | {len(c['manual'])} | {len(c['pending'])} | {missing} | {bad} | "
                     f"{c['checked_by'] or 'not checked: the mobile suite did not run'} |")
    for side, c in cov.items():
        problems = [f"no entry: {s}" for s in c["missing"] or []] + [f"no such screen: {s}" for s in c["stale"] or []] + c["invalid"]
        if problems:
            lines += ["", f"**{side}, to fix:**", ""] + [f"- {p}" for p in problems]
        for k, title in (("manual", "manual only"), ("pending", "pending")):
            groups: dict[str, list[str]] = {}
            for item in c[k]:
                name, _, why = item.partition(": ")
                groups.setdefault(why, []).append(name)
            if groups:
                lines += ["", f"**{side}, {title}:**", ""] + [f"- {why}: {', '.join(names)}" for why, names in groups.items()]
    return lines


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
    if rep.get("coverage"):
        lines += coverage_md(rep["coverage"])
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
        "coverage": load_coverage(a.out),
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
