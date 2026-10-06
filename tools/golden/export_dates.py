"""Export G2/G3 date fixtures from the legacy command-center + llm-proxy date code.

    TZ=UTC ../jarvis-command-center/.venv/bin/python tools/golden/export_dates.py \
        --cc ../jarvis-command-center --llm ../jarvis-llm-proxy-api --out fixtures/golden/dates

These are the LEGACY outputs, bugs included. Go fixes the date bugs in docs/cc/03 §8.1–8.6
(D8), so for those cases the Go golden is the corrected spec and these rows document the
intended difference. Everything else must match.
"""

import argparse
import json
import sys
from datetime import datetime as real_datetime
from pathlib import Path

import pytz

# Fixed instants (UTC) covering weekdays, month/year ends, Feb 29 and both US DST transitions.
INSTANTS = [
    "2026-10-05T14:30:00Z",  # Monday afternoon
    "2026-10-06T03:15:00Z",  # Tuesday 23:15 in New York, Monday... edge of day
    "2026-10-07T12:00:00Z",  # Wednesday noon
    "2026-10-08T22:45:00Z",  # Thursday evening
    "2026-10-09T08:00:00Z",  # Friday morning
    "2026-10-10T17:00:00Z",  # Saturday
    "2026-10-11T23:59:00Z",  # Sunday, last minute
    "2026-10-31T23:30:00Z",  # month end
    "2026-12-31T23:30:00Z",  # year end
    "2028-02-29T12:00:00Z",  # leap day
    "2027-03-14T06:30:00Z",  # US spring forward day (01:30 EST)
    "2027-11-07T05:30:00Z",  # US fall back day (01:30 EDT, first pass)
]
ZONES = ["UTC", "America/New_York", "Asia/Kolkata", "UTC+05:30", None, "Not/AZone"]


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


def jsonable(v):
    return json.loads(json.dumps(v, default=str))


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--cc", required=True, type=Path)
    ap.add_argument("--llm", required=True, type=Path)
    ap.add_argument("--out", required=True, type=Path)
    args = ap.parse_args()
    sys.path.insert(0, str(args.cc.resolve()))

    from app.core import date_resolution, general_context

    # Only the static vocabulary is needed; read ALL_DATE_KEYS without importing model code.
    src = (args.llm / "services" / "date_keys.py").read_text()
    ns: dict = {}
    start = src.index("ALL_DATE_KEYS")
    # The vocabulary is defined as plain literals near the top of the module.
    head = src[: src.index("\ndef ", start)]
    head = "\n".join(l for l in head.splitlines() if not l.startswith("from services"))
    exec(compile(head, "date_keys_head", "exec"), ns)
    keys = sorted(ns["ALL_DATE_KEYS"])
    keys_extra = ["in_30_minutes", "in_2_hours", "in_1_hour", "in_90_minutes", "In 2 Hours", "next week", "Tomorrow:Morning"]

    out = args.out
    out.mkdir(parents=True, exist_ok=True)
    contexts, resolutions = [], []
    for iso in INSTANTS:
        instant = real_datetime.fromisoformat(iso.replace("Z", "+00:00"))
        frozen = frozen_datetime(instant)
        general_context.datetime = frozen
        date_resolution.datetime = frozen
        for zone in ZONES:
            try:
                ctx = general_context.generate_date_context_object(zone)
            except Exception as e:  # legacy raises on some zones; record it (a D8 bug to fix)
                contexts.append({"now": iso, "timezone": zone, "error": f"{type(e).__name__}: {e}"})
                continue
            contexts.append({"now": iso, "timezone": zone, "date_context": jsonable(ctx)})
            if zone not in ("America/New_York", "UTC"):
                continue
            for k in keys + keys_extra:
                r, u = date_resolution.resolve_date_keys([k], ctx)
                resolutions.append({"now": iso, "timezone": zone, "keys": [k], "resolved": r, "unresolved": u})
            for combo in (["tomorrow", "morning"], ["this_weekend"], ["today", "tonight"], ["next_monday", "evening"]):
                r, u = date_resolution.resolve_date_keys(combo, ctx)
                resolutions.append({"now": iso, "timezone": zone, "keys": combo, "resolved": r, "unresolved": u})

    norm = {k: date_resolution.normalize_date_key(k) for k in ["Tomorrow", " next week ", "TOMORROW:MORNING", "in 2 hours", "this_weekend"]}
    (out / "vocabulary.json").write_text(json.dumps({"static_keys": keys}, indent=1) + "\n")
    (out / "date_context.json").write_text(json.dumps(contexts, indent=1, ensure_ascii=False) + "\n")
    (out / "resolution.json").write_text(json.dumps(resolutions, indent=1, ensure_ascii=False) + "\n")
    (out / "normalize.json").write_text(json.dumps(norm, indent=1) + "\n")
    print(f"{len(keys)} keys, {len(contexts)} contexts, {len(resolutions)} resolutions -> {out}")


if __name__ == "__main__":
    main()
