"""Tiny fake ComfyUI for harness tests. Implements only: POST /prompt, GET /history/<id>, GET /view, POST /upload/image.
Prompt N takes (cold_s if N==1 else warm_s) seconds; while busy it writes a high number into --vram-file (read by the
stub nvidia-smi), idle writes a low one. --fail-at N makes prompt N end with status_str=error; --reject-at N makes
POST /prompt return HTTP 400 with node_errors. Saves every submitted graph to --log (one JSON per line)."""
import argparse
import json
import struct
import threading
import time
import zlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse


def png():
    def chunk(t, d):
        c = struct.pack(">I", len(d)) + t + d
        return c + struct.pack(">I", zlib.crc32(t + d) & 0xFFFFFFFF)
    raw = b"\x00" + b"\xc8\x40\x40" * 8
    return (b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", 8, 1, 8, 2, 0, 0, 0))
            + chunk(b"IDAT", zlib.compress(raw)) + chunk(b"IEND", b""))


ap = argparse.ArgumentParser()
ap.add_argument("--port", type=int, required=True)
ap.add_argument("--cold-s", type=float, default=1.5)
ap.add_argument("--warm-s", type=float, default=0.5)
ap.add_argument("--fail-at", type=int, default=0)
ap.add_argument("--reject-at", type=int, default=0)
ap.add_argument("--vram-file", required=True)
ap.add_argument("--log", required=True)
A = ap.parse_args()
LOCK = threading.Lock()
JOBS = {}  # id -> (done_at, ok)
N = [0]
IMG = png()


def set_vram(v):
    with open(A.vram_file, "w") as f:
        f.write(str(v))


class H(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def send(self, code, body, ctype="application/json"):
        if not isinstance(body, bytes):
            body = json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        if self.path == "/upload/image":
            return self.send(200, {"name": "uploaded.png", "subfolder": "", "type": "input"})
        if self.path != "/prompt":
            return self.send(404, {})
        with LOCK:
            N[0] += 1
            n = N[0]
            if n == A.reject_at:
                return self.send(400, {"error": {"type": "prompt_outputs_failed_validation"},
                                       "node_errors": {"5": {"errors": [{"message": "bad input"}]}}})
            with open(A.log, "a") as f:
                f.write(json.dumps(json.loads(body)["prompt"]) + "\n")
            pid = f"fake-{n}"
            dur = A.cold_s if n == 1 else A.warm_s
            JOBS[pid] = (time.time() + dur, n != A.fail_at)
            set_vram(7000)
        threading.Timer(dur, lambda: set_vram(1200)).start()
        self.send(200, {"prompt_id": pid, "number": n, "node_errors": {}})

    def do_GET(self):
        u = urlparse(self.path)
        if u.path.startswith("/history/"):
            pid = u.path.rsplit("/", 1)[1]
            job = JOBS.get(pid)
            if not job or time.time() < job[0]:
                return self.send(200, {})
            st = {"status_str": "success" if job[1] else "error", "completed": job[1], "messages": []}
            out = {"7": {"images": [{"filename": f"{pid}.png", "subfolder": "m1b", "type": "output"}]}} if job[1] else {}
            return self.send(200, {pid: {"status": st, "outputs": out}})
        if u.path == "/view":
            if parse_qs(u.query).get("type") != ["output"]:
                return self.send(404, {})
            return self.send(200, IMG, "image/png")
        self.send(404, {})


set_vram(1200)
ThreadingHTTPServer(("127.0.0.1", A.port), H).serve_forever()
