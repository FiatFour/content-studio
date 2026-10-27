"""Throwaway M0 probe for D4. Talks to a local ComfyUI at 127.0.0.1:8188.
Usage: comfy_probe.py <checkpoint> <steps> <cfg> <sampler> <scheduler> <WxH> <tag>
Generates the fixed shot list, polling nvidia-smi for peak VRAM, and prints seconds per image."""
import json
import subprocess
import sys
import threading
import time
import urllib.request

API = "http://127.0.0.1:8188"
ckpt, steps, cfg, sampler, scheduler, size, tag = sys.argv[1:8]
w, h = map(int, size.split("x"))

STYLE = ("adult animated sitcom style, original character design, thick black outlines, flat cel shading, "
         "bold saturated colors, expressive cartoon faces, clean vector look, vertical cinematic framing")
NEG = "photo, realistic, 3d render, blurry, text, watermark, logo, signature, extra fingers, deformed"
SHOTS = [
    "an elderly Thai grandmother in a floral blouse squinting at a smartphone, cozy wooden Thai house living room",
    "a cheerful Thai teenage boy in a school uniform pointing at a phone screen, sitting on a sofa",
    "close-up of the grandmother's shocked face as the phone starts ringing, comedic exaggerated expression",
    "grandmother and grandson laughing together on a porch at sunset, potted plants, warm light",
]

peak = 0
stop = False


def poll():
    global peak
    while not stop:
        r = subprocess.run(["nvidia-smi", "--query-gpu=memory.used", "--format=csv,noheader,nounits"],
                           capture_output=True, text=True)
        peak = max(peak, int(r.stdout.strip()))
        time.sleep(0.25)


def graph(prompt, seed):
    return {
        "1": {"class_type": "CheckpointLoaderSimple", "inputs": {"ckpt_name": ckpt}},
        "2": {"class_type": "CLIPTextEncode", "inputs": {"clip": ["1", 1], "text": f"{STYLE}, {prompt}"}},
        "3": {"class_type": "CLIPTextEncode", "inputs": {"clip": ["1", 1], "text": NEG}},
        "4": {"class_type": "EmptyLatentImage", "inputs": {"width": w, "height": h, "batch_size": 1}},
        "5": {"class_type": "KSampler", "inputs": {"model": ["1", 0], "positive": ["2", 0], "negative": ["3", 0],
                                                   "latent_image": ["4", 0], "seed": seed, "steps": int(steps),
                                                   "cfg": float(cfg), "sampler_name": sampler,
                                                   "scheduler": scheduler, "denoise": 1.0}},
        "6": {"class_type": "VAEDecode", "inputs": {"samples": ["5", 0], "vae": ["1", 2]}},
        "7": {"class_type": "SaveImage", "inputs": {"images": ["6", 0], "filename_prefix": f"m0/{tag}"}},
    }


def run(prompt, seed):
    body = json.dumps({"prompt": graph(prompt, seed)}).encode()
    pid = json.loads(urllib.request.urlopen(urllib.request.Request(f"{API}/prompt", body,
                                                                   {"Content-Type": "application/json"})).read())["prompt_id"]
    while True:
        hist = json.loads(urllib.request.urlopen(f"{API}/history/{pid}").read())
        if pid in hist:
            st = hist[pid]["status"]
            if st.get("status_str") != "success":
                raise RuntimeError(json.dumps(st)[:800])
            return
        time.sleep(0.2)


threading.Thread(target=poll, daemon=True).start()
times = []
for i, p in enumerate(SHOTS):
    t = time.time()
    run(p, 1000 + i)
    times.append(round(time.time() - t, 1))
    print(f"{tag} shot{i+1} {times[-1]}s peak_vram={peak}MiB", flush=True)
stop = True
print(json.dumps({"tag": tag, "ckpt": ckpt, "size": size, "steps": int(steps), "first_s": times[0],
                  "warm_s": times[1:], "peak_vram_mib": peak}))
