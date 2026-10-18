# Golden for pinode.Ambient: the Python node's stats (N:scripts/mqtt_tts_listener.py
# handle_measure_ambient_noise: numpy float32 percentiles, round() halving to even) on fixed
# RMS series. Needs numpy only:
#   python3 tools/golden/export_ambient.py > internal/agent/pinode/testdata/ambient_golden.json
import json, numpy as np
rng = np.random.default_rng(171)
series = [
    [50.0],
    [100.0, 200.0],
    [10.0, 20.0, 30.0, 40.0],
    # p75 * 1.3 / 100 is exactly 32.5 and 58.5: Python's round() halves to even (3200, 5800),
    # where Go's math.Round would give 3300 and 5900
    [2500.0] * 4,
    [4400.0, 4500.0, 4500.0, 4500.0, 4600.0],
    # 45.5 rounds up to even: 4600
    [3500.0] * 3,
    [5.0] * 13,           # far below the floor: 1000
    [40000.0] * 13,       # above the ceiling: 10000
] + [list(map(float, rng.gamma(2.0, 400.0, size=n).round(3))) for n in (13, 25, 38, 62, 125)]
out = []
for s in series:
    a = np.array(s, dtype=np.float32)
    p50 = float(np.percentile(a, 50)); p75 = float(np.percentile(a, 75)); p95 = float(np.percentile(a, 95))
    mx = float(np.max(a))
    sug = int(round(p75 * 1.3 / 100.0)) * 100
    sug = max(1000, min(10000, sug))
    out.append({"rms": s, "p50": p50, "p75": p75, "p95": p95, "max": mx, "suggested": sug})
print(json.dumps(out, indent=1))
