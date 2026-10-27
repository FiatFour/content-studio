#!/usr/bin/env bash
# Throwaway M0 spike for D11. Reruns the fixture, both render backends, and probes the outputs.
# Usage: spikes/m0-render/run.sh <scratch-dir>
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
out=${1:?scratch dir}
mkdir -p "$out"
"$here/make_fixture.sh" "$out/fixture"
python3 - "$out/fixture" > "$out/spec.json" <<'PY'
import json, sys
F = sys.argv[1]
moves = [("in", ""), ("in", "right"), ("out", ""), ("in", "up"), ("out", "left"), ("in", "down")]
print(json.dumps({
    "shots": [{"image": f"{F}/shot{i+1}.png", "duration": 10, "move": {"zoom": z, "amount": 1.2, "pan": p}}
              for i, (z, p) in enumerate(moves)],
    "audio": f"{F}/dialogue.wav", "subtitles": f"{F}/subs.th.srt",
    "font": "Noto Sans Thai", "font_size": 20, "width": 1080, "height": 1920, "fps": 30}, indent=1))
PY
(cd "$here" && go build -o "$out/spike" .)
for b in plain skill; do
  /usr/bin/time -f "$b wall=%es cpu=%P maxrss=%MKB" "$out/spike" -backend "$b" -spec "$out/spec.json" -o "$out/out_$b.mp4"
  ffprobe -v error -show_entries stream=codec_type,width,height,r_frame_rate:format=duration -of compact "$out/out_$b.mp4"
done
