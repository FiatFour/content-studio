"""Throwaway M0 baseline for D5: facebook/mms-tts-tha (CC-BY-NC 4.0, single voice). Usage: mms_probe.py <out_dir>"""
import json
import os
import sys
import time

import soundfile as sf
import torch
from transformers import AutoTokenizer, VitsModel

sys.path.insert(0, os.path.dirname(__file__))
from lines import LINES

out = sys.argv[1]
os.makedirs(out, exist_ok=True)
t0 = time.time()
model = VitsModel.from_pretrained("facebook/mms-tts-tha").to("cuda")
tok = AutoTokenizer.from_pretrained("facebook/mms-tts-tha")
load_s = time.time() - t0
rows = []
for i, (speaker, text) in enumerate(LINES):
    t = time.time()
    with torch.no_grad():
        wav = model(**tok(text, return_tensors="pt").to("cuda")).waveform[0].cpu().numpy()
    gen_s = time.time() - t
    path = os.path.join(out, f"line{i+1:02d}_{speaker}.wav")
    sf.write(path, wav, model.config.sampling_rate)
    dur = len(wav) / model.config.sampling_rate
    rows.append({"line": i + 1, "speaker": speaker, "text": text, "file": path,
                 "audio_s": round(dur, 2), "gen_s": round(gen_s, 2), "rtf": round(gen_s / dur, 3)})
summary = {"engine": "mms-tts-tha", "load_s": round(load_s, 1),
           "torch_peak_alloc_mib": round(torch.cuda.max_memory_allocated() / 2**20), "lines": rows}
json.dump(summary, open(os.path.join(out, "timings.json"), "w"), ensure_ascii=False, indent=1)
print({k: v for k, v in summary.items() if k != "lines"}, [r["audio_s"] for r in rows])
