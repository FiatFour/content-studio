"""Throwaway M0 scorer for D5: transcribe each clip with Whisper and report the character error
rate against the script text. An intelligibility proxy only; naturalness needs a human ear.
Usage: score.py <timings.json> [whisper_model]"""
import json
import sys

import whisper

summary = json.load(open(sys.argv[1]))
model = whisper.load_model(sys.argv[2] if len(sys.argv) > 2 else "turbo")


def norm(s):
    return "".join(ch for ch in s if "฀" <= ch <= "๿" or ch.isalnum())


def cer(ref, hyp):
    prev = list(range(len(hyp) + 1))
    for i, r in enumerate(ref, 1):
        cur = [i]
        for j, h in enumerate(hyp, 1):
            cur.append(min(prev[j] + 1, cur[j - 1] + 1, prev[j - 1] + (r != h)))
        prev = cur
    return prev[-1] / max(len(ref), 1)


errs, chars = 0.0, 0
for row in summary["lines"]:
    hyp = model.transcribe(row["file"], language="th", fp16=True)["text"]
    ref = norm(row["text"])
    c = cer(ref, norm(hyp))
    errs += c * len(ref)
    chars += len(ref)
    row["asr"], row["cer"] = hyp, round(c, 3)
    print(f'{row["line"]:>2} {row["speaker"]:<8} cer={c:.2f} asr={hyp}')
summary["cer_total"] = round(errs / chars, 3)
json.dump(summary, open(sys.argv[1], "w"), ensure_ascii=False, indent=1)
print(summary["engine"], "overall CER", summary["cer_total"])
