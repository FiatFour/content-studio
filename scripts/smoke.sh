#!/usr/bin/env bash
# Creates one real 60 s episode through the API, waits for it, and checks the MP4 with ffprobe.
# Needs `make dev` running.
set -euo pipefail
cd "$(dirname "$0")/.."
API=${STUDIO_API:-http://127.0.0.1:8080/api}
auth=(-H 'Content-Type: application/json')

resp=$(curl -sf "${auth[@]}" -X POST "$API/episodes" -d '{"title":"smoke test","idea":"คุณยายหัดใช้สมาร์ทโฟนครั้งแรกและโทรหาลุงโดยไม่ได้ตั้งใจ","target_duration_s":60}')
ep=$(echo "$resp" | python3 -c 'import json,sys;print(json.load(sys.stdin)["episode_id"])')
echo "episode $ep created"
while :; do
  d=$(curl -sf "$API/episodes/$ep")
  state=$(echo "$d" | python3 -c 'import json,sys;print(json.load(sys.stdin)["runs"][0]["state"])')
  stage=$(echo "$d" | python3 -c 'import json,sys;r=json.load(sys.stdin)["runs"][0];j=[x for x in r["jobs"] if x["state"]=="running"];print(j[0]["stage"]+" "+str(j[0]["progress_done"])+"/"+str(j[0]["progress_total"]) if j else "-")')
  echo "$(date +%T) $state $stage"
  case $state in ready_for_review) break ;; failed|cancelled) echo "$d" | python3 -m json.tool | grep -E 'error_(class|message)'; exit 1 ;; esac
  sleep 10
done
vid=$(echo "$d" | python3 -c 'import json,sys;print(json.load(sys.stdin)["videos"][0]["id"])')
out=var/smoke-$ep.mp4
curl -sf "$API/artifacts/$vid/content" -o "$out"
ffprobe -v error -show_entries stream=codec_type,codec_name,width,height,r_frame_rate:format=duration -of compact "$out"
echo "saved $out (video artifact $vid); open http://127.0.0.1:8080/#/review/$vid to review"
