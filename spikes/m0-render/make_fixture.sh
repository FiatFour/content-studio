#!/usr/bin/env bash
set -euo pipefail
out=${1:?fixture dir}
mkdir -p "$out"
colors=(0xF28DB2 0x59C9B9 0xB49AF2 0xF2C94C 0xF29B5C 0x4DA3FF)
for i in 0 1 2 3 4 5; do
  ffmpeg -v error -y -f lavfi -i "gradients=s=1440x2560:c0=${colors[$i]}:c1=0x111D33:n=2:speed=0,format=rgb24" \
    -vf "drawgrid=w=180:h=180:t=6:c=0x1B1B1B@0.8,drawtext=fontfile=/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf:text='SHOT $((i+1))':fontsize=220:fontcolor=white:borderw=12:bordercolor=0x1B1B1B:x=(w-tw)/2:y=(h-th)/2" \
    -frames:v 1 "$out/shot$((i+1)).png"
done
ffmpeg -v error -y -f lavfi -i "sine=f=220:d=60:sample_rate=48000" -f lavfi -i "anoisesrc=d=60:c=pink:a=0.05:r=48000" \
  -filter_complex "[0][1]amix=inputs=2,volume='if(lt(mod(t,7.5),5.5),1,0)':eval=frame,aformat=channel_layouts=mono" "$out/dialogue.wav"
python3 - "$out/subs.th.srt" <<'PY'
import sys
lines=["อ้าว... นี่มันอะไรกันเนี่ย หลานจ๋า","ยายครับ ลองกดตรงนี้ดูครับ","ไอ้เครื่องนี้มันจะกดตรงไหนให้เห็นรูปหลานล่ะลูกยายงงไปหมดแล้วนะ",
"ปัดขึ้นนิดเดียวครับยาย","โอ๊ย! ทำไมมันโทรออกไปหาลุงสมชายล่ะ","ไม่เป็นไรครับ กดวางสายตรงปุ่มสีแดง","ยายว่ายายเขียนจดหมายเหมือนเดิมดีกว่า","เดี๋ยวผมสอนใหม่ทุกวันเลยครับยาย"]
def ts(s): h,m=divmod(int(s),3600); m,sec=divmod(m,60); return f"{h:02}:{m:02}:{sec:02},{int(round((s%1)*1000)):03}"
with open(sys.argv[1],"w",encoding="utf-8") as f:
    for i,t in enumerate(lines):
        st=i*7.5; f.write(f"{i+1}\n{ts(st)} --> {ts(st+5.5)}\n{t}\n\n")
PY
