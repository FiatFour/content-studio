"""Throwaway M0 probe for D5. Usage: voxcpm_probe.py <out_dir>
Designs one voice per character from a text description, then voices every line by cloning that
character's first clip, so each character keeps one fixed voice. Writes timings.json."""
import json
import os
import subprocess
import sys
import time

import soundfile as sf
import torch
from voxcpm import VoxCPM

sys.path.insert(0, os.path.dirname(__file__))
from lines import LINES, VOICES

out = sys.argv[1]
os.makedirs(out, exist_ok=True)


def vram_mib():
    r = subprocess.run(["nvidia-smi", "--query-gpu=memory.used", "--format=csv,noheader,nounits"],
                       capture_output=True, text=True)
    return int(r.stdout.strip())


t0 = time.time()
model = VoxCPM.from_pretrained("openbmb/VoxCPM2", load_denoiser=False, optimize=False)
load_s = time.time() - t0
sr = model.tts_model.sample_rate
vram_loaded = vram_mib()

refs, rows, peak = {}, [], vram_loaded
for i, (speaker, text) in enumerate(LINES):
    torch.manual_seed(42)
    t = time.time()
    if speaker not in refs:
        wav = model.generate(text=f"({VOICES[speaker]}){text}", cfg_value=2.0, inference_timesteps=10)
    else:
        wav = model.generate(text=text, reference_wav_path=refs[speaker], cfg_value=2.0, inference_timesteps=10)
    gen_s = time.time() - t
    path = os.path.join(out, f"line{i+1:02d}_{speaker}.wav")
    sf.write(path, wav, sr)
    refs.setdefault(speaker, path)
    peak = max(peak, vram_mib())
    dur = len(wav) / sr
    rows.append({"line": i + 1, "speaker": speaker, "text": text, "file": path,
                 "audio_s": round(dur, 2), "gen_s": round(gen_s, 2), "rtf": round(gen_s / dur, 2)})
    print(json.dumps(rows[-1], ensure_ascii=False), flush=True)

summary = {"engine": "VoxCPM2", "load_s": round(load_s, 1), "vram_loaded_mib": vram_loaded, "vram_peak_mib": peak,
           "torch_peak_alloc_mib": round(torch.cuda.max_memory_allocated() / 2**20), "sample_rate": sr, "lines": rows}
json.dump(summary, open(os.path.join(out, "timings.json"), "w"), ensure_ascii=False, indent=1)
print({k: v for k, v in summary.items() if k != "lines"})
