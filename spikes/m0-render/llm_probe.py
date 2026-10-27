"""Throwaway M0 probe for D3. Usage: python3 llm_probe.py <model> <runs>
Prints one line per run: tokens/s, load and total time, schema validity, and VRAM after the call."""
import json
import os
import subprocess
import sys
import urllib.request

model, runs = sys.argv[1], int(sys.argv[2])
schema = json.load(open(os.path.join(os.path.dirname(__file__), "script-schema.json")))
prompt = ("เขียนบทซิทคอมแนวชีวิตต้นฉบับ ความยาว 60 วินาที เรื่อง: คุณยายเรียนใช้สมาร์ทโฟนครั้งแรก. "
          "ตอบเป็น JSON ตาม schema. บทพูดเป็นภาษาไทย image_prompt เป็นภาษาอังกฤษ")
for i in range(runs):
    body = {"model": model, "stream": False, "think": False, "format": schema, "prompt": prompt,
            "options": {"num_ctx": 4096, "temperature": 0.7}}
    req = urllib.request.Request("http://127.0.0.1:11434/api/generate", json.dumps(body).encode(),
                                 {"Content-Type": "application/json"})
    d = json.loads(urllib.request.urlopen(req, timeout=600).read())
    try:
        s = json.loads(d["response"])
        lines = [l for sc in s["scenes"] for sh in sc["shots"] for l in sh["lines"]]
        verdict = f"valid lines={len(lines)}"
    except (ValueError, KeyError, TypeError) as e:
        verdict = f"INVALID {e}"
    vram = subprocess.run(["nvidia-smi", "--query-gpu=memory.used", "--format=csv,noheader"],
                          capture_output=True, text=True).stdout.strip()
    print(f"{model} run{i+1} tok/s={d['eval_count'] / (d['eval_duration'] / 1e9):.1f} "
          f"load={d['load_duration'] / 1e9:.1f}s total={d['total_duration'] / 1e9:.1f}s {verdict} vram={vram}")
