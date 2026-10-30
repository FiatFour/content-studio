import { describe, expect, it } from "vitest";
import { dictionaries } from "./i18n";
import { carCentreY, carFloor, FLOOR_CODES, floorBox, FLOOR_H, ROOF, visualOf, trackerDots, teamRows, countText, pipelineSteps, reviewHref, type Floor, type FloorState } from "./tower";

const floor = (code: string, state: FloorState): Floor => ({
  code, agent: "x", state, episodes: [], tasks_completed: null, handoff_from: null, handoff_to: null,
  provider: null, model: null, last_log: null, progress: null,
});
const floors = (m: Record<string, FloorState> = {}) => FLOOR_CODES.map((c) => floor(c, m[c] ?? "idle"));
const ep = (f: string | null) => ({ run_id: 1, run_state: "running", floor: f, stages: { done: 1, total: 8 }, current: null });

describe("visualOf", () => {
  it("gives every state its own glyph, so colour is never the only cue", () => {
    const states: FloorState[] = ["idle", "active", "done", "failed", "waiting", "not_implemented"];
    expect(new Set(states.map((s) => visualOf(s).glyph)).size).toBe(states.length);
  });
  it("uses blue for done, green for active, grey for ahead (spec lamps)", () => {
    expect(visualOf("done").lamp).toBe("blue");
    expect(visualOf("active").lamp).toBe("green");
    expect(visualOf("idle").lamp).toBe("grey");
  });
  it("treats an unknown state as idle", () => expect(visualOf("bogus")).toEqual(visualOf("idle")));
});

describe("carFloor", () => {
  it("follows the active episode's crate", () =>
    expect(carFloor({ floors: floors({ "3F": "active" }), active_episode: ep("4F") })).toBe("4F"));
  it("falls back to the working floor, then failed, then waiting", () => {
    expect(carFloor({ floors: floors({ "2F": "active", "1F": "failed" }), active_episode: null })).toBe("2F");
    expect(carFloor({ floors: floors({ "1F": "failed", "5F": "waiting" }), active_episode: null })).toBe("1F");
    expect(carFloor({ floors: floors({ "6F": "waiting" }), active_episode: null })).toBe("6F");
  });
  it("is null when nothing runs, and ignores a floor the payload does not have", () => {
    expect(carFloor({ floors: floors({ "1F": "done" }), active_episode: null })).toBeNull();
    expect(carFloor({ floors: floors(), active_episode: ep("9F") })).toBeNull();
  });
});

describe("trackerDots", () => {
  it("maps done/active/ahead to check/pulse/hollow, bottom to top, roof last", () => {
    const d = trackerDots(floors({ "1F": "done", "2F": "done", "3F": "active", "4F": "idle", "6F": "not_implemented" }));
    expect(d.map((x) => x.code)).toEqual(["1F", "2F", "3F", "4F", "5F", "6F", "Roof"]);
    expect(d.map((x) => x.kind)).toEqual(["check", "check", "pulse", "hollow", "hollow", "hollow", "hollow"]);
  });
  it("draws a solid line only above a finished floor and always dashes into the roof", () => {
    const d = trackerDots(floors({ "1F": "done", "2F": "done", "3F": "done", "4F": "done", "5F": "done", "6F": "done" }));
    expect(d.map((x) => x.solidBelow)).toEqual([false, true, true, true, true, true, false]);
  });
  it("skips floors the payload lacks", () => expect(trackerDots([floor("1F", "done")]).map((x) => x.code)).toEqual(["1F"]));
  it("keeps dots in increasing height order", () => {
    const d = trackerDots(floors());
    for (let i = 1; i < d.length; i++) expect(d[i].yPct).toBeLessThan(d[i - 1].yPct);
  });
});

describe("teamRows / countText", () => {
  it("shows a null count as a dash, zero as 0, never invents a number", () => {
    const f = floors(); f[1] = { ...f[1], tasks_completed: 0 }; f[2] = { ...f[2], tasks_completed: 7 };
    const r = teamRows(f);
    expect(r).toHaveLength(FLOOR_CODES.length);
    expect(r.map((x) => countText(x.tasks)).slice(0, 3)).toEqual(["–", "0", "7"]);
  });
});

describe("pipelineSteps / reviewHref", () => {
  it("is empty without an active episode and flags only the last step", () => {
    expect(pipelineSteps(null)).toEqual([]);
    const e = { ...ep("1F"), pipeline: ["script", "images", "review", "export"].map((s) => ({ stage: s, agent: s, floor: "x", state: "pending", progress: null })) };
    const st = pipelineSteps(e);
    expect(st.map((x) => x.last)).toEqual([false, false, false, true]);
    expect(st[3].stage).toBe("export");
  });
  it("links the clip to the existing review page by artifact id", () => expect(reviewHref({ artifact_id: 12 })).toBe("#/review/12"));
});

describe("export wording", () => {
  it("labels the last step Export and never Publish, in both languages", () => {
    expect(dictionaries.en["tower.stage.export"]).toBe("Export");
    for (const lang of ["en", "th"] as const)
      for (const [k, v] of Object.entries(dictionaries[lang]).filter(([k]) => k.startsWith("tower."))) expect(v, `${lang}:${k}`).not.toMatch(/publish|\u0e40\u0e1c\u0e22\u0e41\u0e1e\u0e23\u0e48/i);
  });
});

describe("geometry", () => {
  it("stacks floors without gaps from the roof down to B1", () => {
    let y = ROOF.h;
    for (const c of FLOOR_CODES.slice(1)) { expect(floorBox(c).y).toBe(y); y += FLOOR_H; }
    expect(y).toBeLessThan(890);
  });
  it("keeps the car inside its floor", () => {
    for (const c of FLOOR_CODES) { const b = floorBox(c); expect(carCentreY(c)).toBeGreaterThan(b.y); expect(carCentreY(c)).toBeLessThan(b.y + b.h); }
  });
});
