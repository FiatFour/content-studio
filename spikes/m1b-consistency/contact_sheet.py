"""Tile an arm's images into one labeled contact sheet (Pillow).
Usage: contact_sheet.py OUT_DIR [sheet.png] [--refs-dir REFS]
Reads OUT_DIR/results.json; one row per character, one column per scene; optional first column = reference image.
Pillow lives in ~/ai/ComfyUI/.venv (system python has none): run with ~/ai/ComfyUI/.venv/bin/python."""
import argparse
import json
import os
import sys

try:
    from PIL import Image, ImageDraw
except ImportError:
    sys.exit("Pillow not found; run with ~/ai/ComfyUI/.venv/bin/python")

ap = argparse.ArgumentParser()
ap.add_argument("out_dir")
ap.add_argument("sheet", nargs="?")
ap.add_argument("--refs-dir")
ap.add_argument("--thumb-w", type=int, default=240)
a = ap.parse_args()

with open(os.path.join(a.out_dir, "results.json")) as f:
    res = json.load(f)
tw = a.thumb_w
LABEL_H = 34
rows = {}
for im in res["images"]:
    rows.setdefault(im["char"], []).append(im)
if not rows:
    sys.exit("results.json has no images")
ncols = max(len(v) for v in rows.values()) + (1 if a.refs_dir else 0)
cell_h = None
cells = []  # (row, col, image, label)
for r, (cid, ims) in enumerate(rows.items()):
    c = 0
    if a.refs_dir:
        p = os.path.join(a.refs_dir, f"{cid}.png")
        if os.path.isfile(p):
            cells.append((r, 0, Image.open(p).convert("RGB"), f"{cid} REFERENCE"))
        c = 1
    for im in ims:
        cells.append((r, c, Image.open(os.path.join(a.out_dir, im["file"])).convert("RGB"),
                      f'{cid}/{im["scene"]} seed {im["seed"]} {im["seconds"]}s{" cold" if im["cold"] else ""}'))
        c += 1
first = cells[0][2]
th = round(tw * first.height / first.width)
sheet = Image.new("RGB", (ncols * tw, len(rows) * (th + LABEL_H)), "white")
d = ImageDraw.Draw(sheet)
for r, c, img, label in cells:
    x, y = c * tw, r * (th + LABEL_H)
    sheet.paste(img.resize((tw, th)), (x, y + LABEL_H))
    d.text((x + 3, y + 3), f'arm {res["arm"]}', fill="black")
    d.text((x + 3, y + 17), label[: tw // 6], fill="black")
dest = a.sheet or os.path.join(a.out_dir, f'contact_{res["arm"]}.png')
sheet.save(dest)
print(f"wrote {dest} ({sheet.width}x{sheet.height}, {len(cells)} tiles)")
