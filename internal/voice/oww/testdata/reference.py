# Python reference for oww_test.go: openWakeWord 0.6.0's own scores, frame by frame.
#
# Inputs are Kokoro renders at 24 kHz (kokoro-multi-lang-v1_0, lexicon-gb-en, speed 1.0):
# pos_21 / pos_26 = "Hey Jarvis." in voices 21 (Emma) and 26 (George); neg_21 / neg_26 = "Tell
# me a joke.". Each is resampled to 16 kHz, padded with 2 s of seeded noise before and 1 s after
# (the noise depends on clip order, so keep it), written out, and scored in 1280-sample chunks.
# testdata/ keeps pos_21, pos_26 and neg_26 (renamed) and their scores.
#
#   uv run --python 3.11 --with openwakeword==0.6.0 --with onnxruntime --with scipy --with numpy \
#     python reference.py <kokoro clips dir> <oww models dir> <out dir> pos_21 pos_26 neg_21 neg_26
import sys, os, json, wave
import numpy as np
from scipy.signal import resample_poly
import openwakeword
from openwakeword.model import Model

clips, models, out = sys.argv[1], sys.argv[2], sys.argv[3]
os.makedirs(out, exist_ok=True)
rng = np.random.default_rng(1234)
def noise(sec): return rng.normal(0, 30, int(sec * 16000)).astype(np.int16)
def load(p):
    w = wave.open(p); sr = w.getframerate()
    x = np.frombuffer(w.readframes(w.getnframes()), dtype=np.int16).astype(np.float64)
    assert sr == 24000
    return np.clip(np.round(resample_poly(x, 2, 3)), -32768, 32767).astype(np.int16)
res = {"openwakeword": openwakeword.__version__ if hasattr(openwakeword, "__version__") else "0.6.0", "clips": {}}
for name in sys.argv[4:]:
    x = np.concatenate([noise(2.0), load(os.path.join(clips, name + ".wav")), noise(1.0)])
    w = wave.open(os.path.join(out, name + ".wav"), "wb"); w.setnchannels(1); w.setsampwidth(2); w.setframerate(16000); w.writeframes(x.tobytes()); w.close()
    m = Model(wakeword_models=[os.path.join(models, "hey_jarvis_v0.1.onnx")], inference_framework="onnx",
              melspec_model_path=os.path.join(models, "melspectrogram.onnx"),
              embedding_model_path=os.path.join(models, "embedding_model.onnx"))
    scores = []
    for i in range(0, len(x) - 1280 + 1, 1280):
        p = m.predict(x[i:i+1280])
        scores.append(float(list(p.values())[0]))
    res["clips"][name] = scores
    print(name, len(scores), "max %.4f @%d" % (max(scores), int(np.argmax(scores))))
json.dump(res, open(os.path.join(out, "python_scores.json"), "w"), indent=0)
