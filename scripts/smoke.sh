#!/usr/bin/env bash
# Creates one real 60 s episode through the API, waits for it, and checks the MP4 with ffprobe.
# Needs `make dev` running and an authenticated session (D9): sign in at http://localhost:8080 with your
# passkey, copy the value of the `studio_session` cookie from the browser's dev tools (Application >
# Cookies) and run `STUDIO_SESSION=<value> make smoke`. There is no bypass; the cookie is a real session.
set -euo pipefail
cd "$(dirname "$0")/.."
API=${STUDIO_API:-http://127.0.0.1:8080/api}
: "${STUDIO_SESSION:?set STUDIO_SESSION to your studio_session cookie value (see the header of this script)}"
hdr=$(mktemp); trap 'rm -f "$hdr"' EXIT
( umask 077; printf 'Cookie: studio_session=%s\n' "$STUDIO_SESSION" >"$hdr" )
# State-changing requests must carry an allowed Origin; the cookie goes in a private file, not on the command line.
auth=(-H 'Content-Type: application/json' -H "Origin: ${STUDIO_ORIGIN:-http://localhost:8080}" -H "@$hdr")

resp=$(curl -sf "${auth[@]}" -X POST "$API/episodes" -d '{"title":"smoke test","idea":"คุณยายหัดใช้สมาร์ทโฟนครั้งแรกและโทรหาลุงโดยไม่ได้ตั้งใจ","target_duration_s":60}')
ep=$(echo "$resp" | python3 -c 'import json,sys;print(json.load(sys.stdin)["episode_id"])')
echo "episode $ep created"
while :; do
  d=$(curl -sf "${auth[@]}" "$API/episodes/$ep")
  state=$(echo "$d" | python3 -c 'import json,sys;print(json.load(sys.stdin)["runs"][0]["state"])')
  stage=$(echo "$d" | python3 -c 'import json,sys;r=json.load(sys.stdin)["runs"][0];j=[x for x in r["jobs"] if x["state"]=="running"];print(j[0]["stage"]+" "+str(j[0]["progress_done"])+"/"+str(j[0]["progress_total"]) if j else "-")')
  echo "$(date +%T) $state $stage"
  case $state in ready_for_review) break ;; failed|cancelled) echo "$d" | python3 -m json.tool | grep -E 'error_(class|message)'; exit 1 ;; esac
  sleep 10
done
vid=$(echo "$d" | python3 -c 'import json,sys;print(json.load(sys.stdin)["videos"][0]["id"])')
out=var/smoke-$ep.mp4
curl -sf "${auth[@]}" "$API/artifacts/$vid/content" -o "$out"
ffprobe -v error -show_entries stream=codec_type,codec_name,width,height,r_frame_rate:format=duration -of compact "$out"
echo "saved $out (video artifact $vid); open http://localhost:8080/#/review/$vid to review"
