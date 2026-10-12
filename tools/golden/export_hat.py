"""Export the HAT LED goldens for internal/agent/hat (go test reads testdata/hat_golden.json).

Run on a Pi with the Python node installed (it needs apa102_pi from the node's venv):

    /opt/jarvis-node/.venv/bin/python tools/golden/export_hat.py \
        /opt/jarvis-node/services/respeaker_led_service.py > internal/agent/hat/testdata/hat_golden.json

Patterns come from the node's respeaker_led_service.py (_PATTERNS, scaled as _render does);
frames from apa102_pi's own set_pixel + show, with the SPI writes captured instead of sent.
"""
import importlib.util
import json
import sys
import types

stub = types.ModuleType("jarvis_log_client")
stub.JarvisLogger = lambda **_: types.SimpleNamespace(debug=print, info=print, warning=print)
sys.modules["jarvis_log_client"] = stub
spec = importlib.util.spec_from_file_location("respeaker", sys.argv[1])
svc = importlib.util.module_from_spec(spec)
spec.loader.exec_module(svc)

from apa102_pi.driver.apa102 import APA102, RGB_MAP  # noqa: E402


def frame(pixels):
    strip = APA102.__new__(APA102)
    strip.num_led, strip.rgb, strip.global_brightness = len(pixels), RGB_MAP["rgb"], 31
    strip.leds = [APA102.LED_START, 0, 0, 0] * len(pixels)
    sent = []
    strip.send_to_spi = lambda data: sent.extend(list(data))
    for i, (r, g, b, br) in enumerate(pixels):
        strip.set_pixel(i, r, g, b, bright_percent=br)
    strip.show()
    return sent


out = {"patterns": [], "frames": []}
times = [0.0, 0.05, 0.1, 0.15, 0.2, 0.34, 0.5, 0.67, 0.99, 1.0, 1.4, 2.3]
for name in svc._PATTERNS:
    for scale in (100, 50, 7, 0):
        for t in times:
            px = svc._PATTERNS[name](t)
            scaled = [(r, g, b, max(0, min(100, int(br * scale / 100)))) for r, g, b, br in px]
            out["patterns"].append({"name": name, "t": t, "scale": scale, "pixels": scaled})
for px in ([(255, 255, 255, 15)] * 3, [(0, 200, 220, 35)] * 3, [(0, 0, 0, 0), (180, 0, 255, 45), (0, 0, 0, 0)],
           [(1, 2, 3, 100)] * 3, [(255, 0, 0, 1)] * 3, [(10, 20, 30, 99)] * 17):
    out["frames"].append({"pixels": px, "bytes": frame(px)})
json.dump(out, sys.stdout, indent=1)
