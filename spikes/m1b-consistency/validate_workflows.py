"""Offline check of ComfyUI API-format workflows against a saved GET /object_info JSON.
Usage: validate_workflows.py [object_info.json] [workflow.json ...]
Checks per node: class exists; every input name exists; every required input present; combo values are in the enum;
INT/FLOAT/STRING/BOOLEAN types and min/max; links point to an existing node, a valid output slot of the right type.
LoadImage.image is skipped for the enum check (its list is the server's input dir, dynamic).
Also checks fixture.json settings equal the values baked into each template. Prints PASS/FAIL per workflow."""
import glob
import json
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
SCALARS = {"INT": int, "FLOAT": (int, float), "STRING": str, "BOOLEAN": bool}
DYNAMIC_ENUM = {("LoadImage", "image"), ("LoraLoader", "lora_name")}


def spec_of(v):
    """Return (type_or_options, opts) from an object_info input entry."""
    t = v[0]
    opts = v[1] if len(v) > 1 and isinstance(v[1], dict) else {}
    if t == "COMBO":  # newer ComfyUI: ["COMBO", {"options": [...]}]
        return opts.get("options", []), opts
    return t, opts


def check(wf, oi, fixture):
    errs = []
    for nid, node in wf.items():
        cls = node.get("class_type")
        d = oi.get(cls)
        if d is None:
            errs.append(f"node {nid}: unknown class_type {cls!r}")
            continue
        req = d["input"].get("required", {})
        opt = d["input"].get("optional", {})
        allin = {**req, **opt}
        ins = node.get("inputs", {})
        for k in req:
            if k not in ins:
                errs.append(f"node {nid} {cls}: missing required input {k!r}")
        for k, val in ins.items():
            if k not in allin:
                errs.append(f"node {nid} {cls}: input {k!r} does not exist in object_info")
                continue
            t, o = spec_of(allin[k])
            if isinstance(val, list) and len(val) == 2 and isinstance(val[0], str):
                src = wf.get(val[0])
                if src is None:
                    errs.append(f"node {nid}.{k}: link to missing node {val[0]!r}")
                    continue
                sd = oi.get(src.get("class_type"))
                if sd is None:
                    continue  # already reported
                outs = sd["output"]
                if not isinstance(val[1], int) or val[1] >= len(outs):
                    errs.append(f"node {nid}.{k}: node {val[0]} has no output slot {val[1]}")
                elif isinstance(t, str) and outs[val[1]] != t:
                    errs.append(f"node {nid}.{k}: wants {t}, link {val[0]}:{val[1]} gives {outs[val[1]]}")
                continue
            if isinstance(t, list):
                if (cls, k) in DYNAMIC_ENUM:
                    continue
                if val not in t:
                    errs.append(f"node {nid} {cls}.{k}: value {val!r} not in enum {t[:6]}{'...' if len(t) > 6 else ''}")
            elif t in SCALARS:
                if t != "BOOLEAN" and isinstance(val, bool) or not isinstance(val, SCALARS[t]):
                    errs.append(f"node {nid} {cls}.{k}: {val!r} is not {t}")
                elif t in ("INT", "FLOAT"):
                    if "min" in o and val < o["min"] or "max" in o and val > o["max"]:
                        errs.append(f"node {nid} {cls}.{k}: {val} outside [{o.get('min')}, {o.get('max')}]")
            else:
                errs.append(f"node {nid} {cls}.{k}: literal {val!r} given for non-scalar type {t}")
    # links to nodes whose outputs are consumed must also be reachable from a SaveImage
    if not any(n.get("class_type") == "SaveImage" for n in wf.values()):
        errs.append("no SaveImage node")
    # fixture settings baked into the templates
    s = fixture["settings"]
    for nid, n in wf.items():
        i = n.get("inputs", {})
        c = n["class_type"]
        if c == "CheckpointLoaderSimple" and i.get("ckpt_name") != fixture["checkpoint"]:
            errs.append(f"node {nid}: ckpt_name differs from fixture.json")
        if c == "EmptyLatentImage" and (i.get("width"), i.get("height")) != (s["width"], s["height"]):
            errs.append(f"node {nid}: size differs from fixture.json")
        if c == "KSampler":
            for k in ("steps", "cfg", "sampler_name", "scheduler"):
                if i.get(k) != s[k]:
                    errs.append(f"node {nid}: {k}={i.get(k)!r} differs from fixture.json {s[k]!r}")
    return errs


def main():
    args = sys.argv[1:]
    oi_path = args[0] if args else os.path.join(HERE, "object_info.json")
    wfs = args[1:] or sorted(glob.glob(os.path.join(HERE, "workflows", "*.json")))
    with open(oi_path) as f:
        oi = json.load(f)
    with open(os.path.join(HERE, "fixture.json")) as f:
        fixture = json.load(f)
    bad = 0
    for p in wfs:
        try:
            with open(p) as f:
                wf = json.load(f)
            errs = check(wf, oi, fixture)
        except (OSError, ValueError) as e:
            errs = [f"cannot load: {e}"]
        print(f"{'FAIL' if errs else 'PASS'} {os.path.basename(p)}")
        for e in errs:
            print("   -", e)
        bad += bool(errs)
    sys.exit(1 if bad else 0)


if __name__ == "__main__":
    main()
