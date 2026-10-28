"""M1b consistency spike harness (throwaway). Talks to a local ComfyUI, one prompt at a time.
One command per arm:
  harness.py --arm REF --workflow workflows/reference_sheet.json --out OUT   (one front-view reference per character)
  harness.py --arm C   --workflow workflows/arm_c_baseline.json  --out OUT   (baseline, plain text-to-image)
  harness.py --arm A   --workflow workflows/arm_a_ipadapter.json --out OUT --refs-dir REFS  (IP-Adapter)
Writes OUT/results.json and the images. The first image of an arm is reported as cold and excluded from the warm median.
Peak VRAM comes from nvidia-smi sampled every second in a background thread for the whole arm.
Template patching is by node title (_meta.title): positive, negative, reference, ipadapter; plus KSampler and SaveImage."""
import argparse
import copy
import json
import os
import statistics
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

HERE = os.path.dirname(os.path.abspath(__file__))
SMI_ARGS = ["--query-gpu=memory.used", "--format=csv,noheader,nounits"]


class HarnessError(Exception):
    pass


class VramSampler(threading.Thread):
    """Samples GPU memory.used (MiB) every `interval` seconds until stop() is called."""

    def __init__(self, smi, interval=1.0):
        super().__init__(daemon=True)
        self.smi, self.interval = smi, interval
        self.samples, self.failures = [], 0
        self._stop_evt = threading.Event()

    def sample_once(self):
        try:
            r = subprocess.run([self.smi, *SMI_ARGS], capture_output=True, text=True, timeout=10)
            if r.returncode != 0:
                raise ValueError(r.returncode)
            # multi-GPU prints one line per GPU; take the max
            v = max(int(x) for x in r.stdout.split() if x.strip())
            self.samples.append(v)
        except (OSError, ValueError, subprocess.SubprocessError):
            self.failures += 1

    def run(self):
        while not self._stop_evt.is_set():
            self.sample_once()
            self._stop_evt.wait(self.interval)

    def stop(self):
        self._stop_evt.set()
        self.join(timeout=15)
        self.sample_once()  # final sample so a short arm still has data

    @property
    def peak(self):
        return max(self.samples) if self.samples else None


class Comfy:
    def __init__(self, base, timeout_s, poll_s=0.2):
        self.base, self.timeout_s, self.poll_s = base.rstrip("/"), timeout_s, poll_s

    def _req(self, req, what):
        try:
            return urllib.request.urlopen(req, timeout=60).read()
        except urllib.error.HTTPError as e:
            body = e.read()[:600].decode("utf-8", "replace")
            raise HarnessError(f"{what}: HTTP {e.code}: {body}")
        except (urllib.error.URLError, OSError) as e:
            raise HarnessError(f"{what}: {e}")

    def get_json(self, path, what):
        return json.loads(self._req(f"{self.base}{path}", what))

    def upload(self, path, name):
        b = uuid.uuid4().hex
        with open(path, "rb") as f:
            data = f.read()
        parts = [f'--{b}\r\nContent-Disposition: form-data; name="image"; filename="{name}"\r\n'
                 f"Content-Type: image/png\r\n\r\n".encode(), data,
                 f'\r\n--{b}\r\nContent-Disposition: form-data; name="overwrite"\r\n\r\ntrue\r\n--{b}--\r\n'.encode()]
        req = urllib.request.Request(f"{self.base}/upload/image", b"".join(parts),
                                     {"Content-Type": f"multipart/form-data; boundary={b}"})
        return json.loads(self._req(req, "upload"))["name"]

    def submit_and_wait(self, graph):
        """Returns (prompt_id, seconds, [image refs]). Seconds = POST /prompt until history reports success."""
        t0 = time.monotonic()
        req = urllib.request.Request(f"{self.base}/prompt", json.dumps({"prompt": graph}).encode(),
                                     {"Content-Type": "application/json"})
        pid = json.loads(self._req(req, "submit"))["prompt_id"]
        while True:
            hist = self.get_json(f"/history/{pid}", "history")
            if pid in hist:
                st = hist[pid].get("status", {})
                if st.get("status_str") != "success":
                    raise HarnessError(f"prompt {pid} failed: {json.dumps(st)[:600]}")
                secs = time.monotonic() - t0
                imgs = [im for o in hist[pid].get("outputs", {}).values() for im in o.get("images", [])
                        if im.get("type") == "output"]
                return pid, secs, imgs
            if time.monotonic() - t0 > self.timeout_s:
                raise HarnessError(f"prompt {pid} not finished after {self.timeout_s}s")
            time.sleep(self.poll_s)

    def view(self, im):
        q = urllib.parse.urlencode({"filename": im["filename"], "subfolder": im.get("subfolder", ""), "type": im["type"]})
        return self._req(f"{self.base}/view?{q}", "view")


def node_by_title(g, title):
    hits = [n for n in g.values() if n.get("_meta", {}).get("title") == title]
    if len(hits) != 1:
        raise HarnessError(f"template must have exactly one node titled {title!r}, found {len(hits)}")
    return hits[0]


def patch(template, *, positive, negative, seed, prefix, ref_image=None, weight=None):
    g = copy.deepcopy(template)
    node_by_title(g, "positive")["inputs"]["text"] = positive
    node_by_title(g, "negative")["inputs"]["text"] = negative
    ks = [n for n in g.values() if n["class_type"] == "KSampler"]
    sv = [n for n in g.values() if n["class_type"] == "SaveImage"]
    if len(ks) != 1 or len(sv) != 1:
        raise HarnessError("template needs exactly one KSampler and one SaveImage")
    ks[0]["inputs"]["seed"] = seed
    sv[0]["inputs"]["filename_prefix"] = prefix
    if ref_image is not None:
        node_by_title(g, "reference")["inputs"]["image"] = ref_image
    if weight is not None:
        node_by_title(g, "ipadapter")["inputs"]["weight"] = weight
    return g


def jobs(fixture, arm):
    style, neg = fixture["style_prompt"], fixture["negative_prompt"]
    out = []
    for ci, c in enumerate(fixture["characters"]):
        if arm == "REF":
            p = f'{style}, {c["appearance"]}, {fixture["reference"]["prompt_suffix"]}'
            out.append(dict(char=c["id"], scene="ref", seed=fixture["reference"]["seed_base"] + ci, positive=p,
                            negative=neg, file=f'ref_{c["id"]}.png'))
        else:
            for s in c["scenes"]:
                out.append(dict(char=c["id"], scene=s["id"], seed=s["seed"], negative=neg,
                                positive=f'{style}, {c["appearance"]}, {s["prompt"]}',
                                file=f'{arm}_{c["id"]}_{s["id"]}.png'))
    return out


def run_arm(args, fixture, template, comfy, sampler):
    res = {"arm": args.arm, "workflow": os.path.relpath(args.workflow), "server": args.server,
           "settings": fixture["settings"], "checkpoint": fixture["checkpoint"],
           "ipadapter_weight": args.weight if args.arm == "A" else None,
           "baseline_vram_mib": None, "peak_vram_mib": None, "vram_sample_failures": 0,
           "cold": None, "warm_median_s": None, "warm_s": [], "images": [], "error": None}
    refs = {}
    try:
        if args.arm == "A":
            for c in fixture["characters"]:
                p = os.path.join(args.refs_dir, f'{c["id"]}.png')
                if not os.path.isfile(p):
                    raise HarnessError(f"reference image missing: {p} (copy the chosen ref_{c['id']}.png there)")
                refs[c["id"]] = comfy.upload(p, f'm1b_ref_{c["id"]}.png')
        for i, j in enumerate(jobs(fixture, args.arm)):
            g = patch(template, positive=j["positive"], negative=j["negative"], seed=j["seed"],
                      prefix=f'm1b/{args.arm}_{j["char"]}_{j["scene"]}', ref_image=refs.get(j["char"]),
                      weight=args.weight if args.arm == "A" else None)
            pid, secs, imgs = comfy.submit_and_wait(g)
            if len(imgs) != 1:
                raise HarnessError(f"prompt {pid}: expected 1 output image, got {len(imgs)}")
            data = comfy.view(imgs[0])
            if not data.startswith(b"\x89PNG"):
                raise HarnessError(f"prompt {pid}: output is not a PNG")
            with open(os.path.join(args.out, j["file"]), "wb") as f:
                f.write(data)
            rec = {"char": j["char"], "scene": j["scene"], "seed": j["seed"], "prompt_id": pid,
                   "seconds": round(secs, 2), "cold": i == 0, "file": j["file"]}
            res["images"].append(rec)
            print(f'{args.arm} {j["char"]}/{j["scene"]} seed={j["seed"]} {secs:.1f}s{" (cold)" if i == 0 else ""} '
                  f"peak_vram={sampler.peak}MiB", flush=True)
    except HarnessError as e:
        res["error"] = str(e)
        print(f"ERROR: {e}", file=sys.stderr)
    return res


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--arm", required=True, choices=["REF", "C", "A"])
    ap.add_argument("--workflow", required=True)
    ap.add_argument("--fixture", default=os.path.join(HERE, "fixture.json"))
    ap.add_argument("--out", required=True, help="output dir (created; must not already hold results.json)")
    ap.add_argument("--refs-dir", help="arm A: dir with <character id>.png reference images")
    ap.add_argument("--server", default="http://127.0.0.1:8188")
    ap.add_argument("--weight", type=float, default=0.7, help="arm A IP-Adapter weight")
    ap.add_argument("--timeout", type=float, default=600, help="per-image timeout in seconds")
    ap.add_argument("--nvidia-smi", default="nvidia-smi")
    ap.add_argument("--vram-interval", type=float, default=1.0)
    args = ap.parse_args()
    if args.arm == "A" and not args.refs_dir:
        ap.error("--refs-dir is required for arm A")
    os.makedirs(args.out, exist_ok=True)
    rp = os.path.join(args.out, "results.json")
    if os.path.exists(rp):
        sys.exit(f"{rp} already exists; use a new --out dir (nothing is overwritten)")
    with open(args.fixture) as f:
        fixture = json.load(f)
    with open(args.workflow) as f:
        template = json.load(f)

    comfy = Comfy(args.server, args.timeout)
    sampler = VramSampler(args.nvidia_smi, args.vram_interval)
    sampler.sample_once()  # idle baseline before the first prompt
    res_base = sampler.samples[0] if sampler.samples else None
    sampler.start()
    started = time.time()
    res = run_arm(args, fixture, template, comfy, sampler)
    sampler.stop()
    res["baseline_vram_mib"], res["peak_vram_mib"] = res_base, sampler.peak
    res["vram_sample_failures"] = sampler.failures
    res["vram_samples"] = len(sampler.samples)
    ims = res["images"]
    if ims:
        res["cold"] = {"seconds": ims[0]["seconds"], "file": ims[0]["file"]}
        res["warm_s"] = [i["seconds"] for i in ims[1:]]
        if res["warm_s"]:
            res["warm_median_s"] = round(statistics.median(res["warm_s"]), 2)
    res["started_unix"], res["wall_total_s"] = round(started), round(time.time() - started, 1)
    with open(rp, "w") as f:
        json.dump(res, f, indent=2)
    print(f"wrote {rp}: {len(ims)} images, cold={res['cold'] and res['cold']['seconds']} s "
          f"warm_median={res['warm_median_s']}s peak_vram={res['peak_vram_mib']}MiB "
          f"(baseline {res['baseline_vram_mib']}MiB)")
    sys.exit(1 if res["error"] else 0)


if __name__ == "__main__":
    main()
