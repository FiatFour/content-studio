"""Local Thai voice service for the studio worker: VoxCPM2 speech (decision D5) and PyThaiNLP word
segmentation for subtitles. Binds to 127.0.0.1 only and handles one request at a time, so it never
holds more than one model on the GPU.

Endpoints (JSON in, JSON out):
  GET  /health
  POST /tts      {text, voice_description? | reference_wav_b64?, seed}  -> {wav_b64}  (16-bit mono PCM)
  POST /segment  {text}                                                 -> {words}
  POST /unload   {}                                                     -> {unloaded}
"""
import base64
import gc
import io
import json
import os
import tempfile
from http.server import BaseHTTPRequestHandler, HTTPServer

import soundfile as sf
import torch
from pythainlp.tokenize import word_tokenize

MODEL_ID = os.environ.get("THAIVOICE_MODEL", "openbmb/VoxCPM2")
HOST, PORT = "127.0.0.1", int(os.environ.get("THAIVOICE_PORT", "8190"))
MAX_TEXT = 400
MAX_BODY = 32 << 20

_model = None


def model():
    global _model
    if _model is None:
        from voxcpm import VoxCPM
        # optimize=False: torch.compile needs a C compiler, which this machine does not have (M0).
        _model = VoxCPM.from_pretrained(MODEL_ID, load_denoiser=False, optimize=False)
    return _model


def unload():
    global _model
    loaded = _model is not None
    _model = None
    gc.collect()
    if torch.cuda.is_available():
        torch.cuda.empty_cache()
    return loaded


def tts(req):
    text = str(req.get("text", "")).strip()
    if not text or len(text) > MAX_TEXT:
        raise ValueError("text must be 1 to %d characters" % MAX_TEXT)
    torch.manual_seed(int(req.get("seed", 0)))
    m = model()
    ref_b64 = req.get("reference_wav_b64")
    if ref_b64:
        with tempfile.NamedTemporaryFile(suffix=".wav") as ref:
            ref.write(base64.b64decode(ref_b64))
            ref.flush()
            wav = m.generate(text=text, reference_wav_path=ref.name, cfg_value=2.0, inference_timesteps=10)
    else:
        desc = str(req.get("voice_description", "")).replace("(", " ").replace(")", " ").strip()
        if not desc:
            raise ValueError("voice_description or reference_wav_b64 is required")
        wav = m.generate(text="(%s)%s" % (desc, text), cfg_value=2.0, inference_timesteps=10)
    buf = io.BytesIO()
    sf.write(buf, wav, m.tts_model.sample_rate, format="WAV", subtype="PCM_16")
    return {"wav_b64": base64.b64encode(buf.getvalue()).decode()}


def segment(req):
    text = str(req.get("text", ""))
    if len(text) > MAX_TEXT:
        raise ValueError("text too long")
    return {"words": word_tokenize(text, engine="newmm", keep_whitespace=True)}


ROUTES = {"/tts": tts, "/segment": segment, "/unload": lambda _: {"unloaded": unload()}}


class Handler(BaseHTTPRequestHandler):
    def _send(self, code, body):
        data = json.dumps(body, ensure_ascii=False).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        if self.path == "/health":
            self._send(200, {"status": "ok", "model": MODEL_ID, "loaded": _model is not None})
        else:
            self._send(404, {"error": "not found"})

    def do_POST(self):
        fn = ROUTES.get(self.path)
        if fn is None:
            return self._send(404, {"error": "not found"})
        n = int(self.headers.get("Content-Length", "0"))
        if n > MAX_BODY:
            return self._send(413, {"error": "request too large"})
        try:
            req = json.loads(self.rfile.read(n) or b"{}")
            self._send(200, fn(req))
        except ValueError as e:
            self._send(400, {"error": str(e)})
        except Exception as e:  # sanitized: the class name only, details go to stderr
            self.log_error("request failed: %r", e)
            self._send(500, {"error": "voice generation failed (%s)" % type(e).__name__})


if __name__ == "__main__":
    HTTPServer((HOST, PORT), Handler).serve_forever()
