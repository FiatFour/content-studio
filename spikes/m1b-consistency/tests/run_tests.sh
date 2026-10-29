#!/bin/sh
# Harness tests against the fake server with nvidia-smi stubbed. No real ComfyUI, no GPU.
# Usage: tests/run_tests.sh   (from anywhere). Needs python3 only; contact sheet step uses the ComfyUI venv python if present.
set -u
D=$(cd "$(dirname "$0")/.." && pwd)
T=$(mktemp -d)
export FAKE_VRAM_FILE="$T/vram"
PORT=18188
fail=0
ok() { echo "ok   - $1"; }
bad() { echo "FAIL - $1"; fail=1; }

start() { # args: extra fake server args
  python3 "$D/tests/fake_comfy.py" --port $PORT --vram-file "$T/vram" --log "$T/graphs.jsonl" "$@" &
  SRV=$!; sleep 0.7
}
stop() { kill $SRV 2>/dev/null; wait $SRV 2>/dev/null; rm -f "$T/graphs.jsonl"; }
H="python3 $D/harness.py --server http://127.0.0.1:$PORT --nvidia-smi $D/tests/fake_nvidia_smi.sh --timeout 10"

echo "== 1. arm C happy path: cold 1.5s, warm 0.5s, vram 7000 busy / 1200 idle"
start --cold-s 1.5 --warm-s 0.5
$H --arm C --workflow $D/workflows/arm_c_baseline.json --out $T/c
rc=$?; stop
[ $rc -eq 0 ] && ok "exit 0" || bad "exit $rc"
python3 - "$T/c/results.json" <<'E' && ok "results.json: 12 images, cold split, warm median ~0.5, peak 7000" || bad "results.json content"
import json, sys
r = json.load(open(sys.argv[1]))
assert len(r["images"]) == 12 and r["error"] is None
assert r["images"][0]["cold"] and not any(i["cold"] for i in r["images"][1:])
assert 1.5 <= r["cold"]["seconds"] < 2.0, r["cold"]
assert len(r["warm_s"]) == 11 and 0.5 <= r["warm_median_s"] < 0.8, r["warm_median_s"]
assert r["peak_vram_mib"] == 7000 and r["baseline_vram_mib"] == 1200, (r["peak_vram_mib"], r["baseline_vram_mib"])
assert r["vram_samples"] >= 5 and r["vram_sample_failures"] == 0
assert all(i["prompt_id"].startswith("fake-") and isinstance(i["seed"], int) for i in r["images"])
print({k: r[k] for k in ("cold", "warm_median_s", "peak_vram_mib", "baseline_vram_mib", "vram_samples", "wall_total_s")})
E
ls "$T/c" | head -3; n=$(ls "$T/c"/C_*.png | wc -l); [ "$n" -eq 12 ] && ok "12 PNGs saved" || bad "png count $n"

echo "== 2. refuses to overwrite an existing results.json"
$H --arm C --workflow $D/workflows/arm_c_baseline.json --out $T/c >/dev/null 2>&1 && bad "overwrote" || ok "second run into same dir refused"

echo "== 3. arm REF: 2 images; arm A: patched graph (weight, reference name, seed, prompt)"
start --cold-s 0.3 --warm-s 0.2
$H --arm REF --workflow $D/workflows/reference_sheet.json --out $T/ref || bad "REF run"
[ -f "$T/ref/ref_yai_nuan.png" ] && [ -f "$T/ref/ref_lung_chai.png" ] && ok "2 reference PNGs" || bad "ref PNGs"
mkdir $T/refs; cp "$T/ref/ref_yai_nuan.png" $T/refs/yai_nuan.png; cp "$T/ref/ref_lung_chai.png" $T/refs/lung_chai.png
rm -f "$T/graphs.jsonl"
$H --arm A --workflow $D/workflows/arm_a_ipadapter.json --out $T/a --refs-dir $T/refs --weight 0.55 || bad "A run"
python3 - "$T/graphs.jsonl" <<'E' && ok "arm A graphs carry weight 0.55, uploaded ref, per-scene seed" || bad "arm A graph patching"
import json, sys
g = [json.loads(l) for l in open(sys.argv[1])]
assert len(g) == 12
assert all(x["11"]["inputs"]["weight"] == 0.55 and x["10"]["inputs"]["image"] == "uploaded.png" for x in g)
assert g[0]["5"]["inputs"]["seed"] == 1101 and g[11]["5"]["inputs"]["seed"] == 2106
assert "Yai Nuan" not in g[0]["2"]["inputs"]["text"] and "grandmother" in g[0]["2"]["inputs"]["text"]
assert "multiple people" in g[0]["3"]["inputs"]["text"]
E
stop
rm -rf "$T/refs"
echo "== 4. arm A without a reference image fails cleanly"
start
$H --arm A --workflow $D/workflows/arm_a_ipadapter.json --out $T/a2 --refs-dir $T/nonexistent 2>"$T/err"; rc=$?
stop
[ $rc -eq 1 ] && grep -q "reference image missing" "$T/err" && ok "exit 1, message: $(cat $T/err)" || bad "missing ref handling rc=$rc"

echo "== 5. prompt 4 ends with status error: arm stops, partial results.json kept, exit 1"
start --cold-s 0.3 --warm-s 0.2 --fail-at 4
$H --arm C --workflow $D/workflows/arm_c_baseline.json --out $T/e1 2>"$T/err"; rc=$?
stop
python3 - "$T/e1/results.json" "$rc" <<'E' && ok "3 images kept, error recorded, exit 1" || bad "error handling"
import json, sys
r = json.load(open(sys.argv[1]))
assert sys.argv[2] == "1" and len(r["images"]) == 3 and "failed" in r["error"], r["error"]
print("error:", r["error"][:90])
E

echo "== 6. HTTP 400 on POST /prompt (validation error) is reported with the body"
start --reject-at 2
$H --arm C --workflow $D/workflows/arm_c_baseline.json --out $T/e2 2>"$T/err"; rc=$?
stop
grep -q "HTTP 400" "$T/err" && grep -q "node_errors" "$T/err" && [ $rc -eq 1 ] && ok "$(cut -c1-110 $T/err)" || bad "400 handling"

echo "== 7. server down: connection refused is an error, not a hang or traceback"
$H --arm C --workflow $D/workflows/arm_c_baseline.json --out $T/e3 2>"$T/err"; rc=$?
grep -q "submit:" "$T/err" && [ $rc -eq 1 ] && ok "$(cut -c1-110 $T/err)" || bad "server down handling"

echo "== 8. per-image timeout: job never finishes"
start --cold-s 30
python3 "$D/harness.py" --server http://127.0.0.1:$PORT --nvidia-smi $D/tests/fake_nvidia_smi.sh --timeout 2 --arm C --workflow $D/workflows/arm_c_baseline.json --out $T/e4 2>"$T/err"; rc=$?
stop
grep -q "not finished after 2" "$T/err" && [ $rc -eq 1 ] && ok "$(cat $T/err)" || bad "timeout handling"

echo "== 9. nvidia-smi failing: arm still runs, peak is null, failures counted"
start --cold-s 0.2 --warm-s 0.1
FAKE_SMI_FAIL=1 $H --arm REF --workflow $D/workflows/reference_sheet.json --out $T/e5 >/dev/null 2>&1
stop
python3 - "$T/e5/results.json" <<'E' && ok "peak null / baseline null / failures counted" || bad "smi failure handling"
import json, sys
r = json.load(open(sys.argv[1]))
print({k: r[k] for k in ("peak_vram_mib", "vram_sample_failures", "baseline_vram_mib")})
assert len(r["images"]) == 2 and r["error"] is None
E

echo "== 10. contact sheet from arm C output"
PY=$HOME/ai/ComfyUI/.venv/bin/python; [ -x "$PY" ] || PY=python3
$PY "$D/contact_sheet.py" "$T/c" "$T/sheet.png" && [ -s "$T/sheet.png" ] && ok "sheet written" || bad "contact sheet"
rm -rf "$T"
[ $fail -eq 0 ] && echo "ALL PASS" || echo "SOME FAILED"
exit $fail
