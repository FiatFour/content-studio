import { describe, expect, it } from "vitest";
import { dictionaries } from "./i18n";
import { carCentreY, carFloor, FLOOR_CODES, floorBox, FLOOR_H, ROOF, visualOf, trackerDots, teamRows, countText, pipelineSteps, reviewHref, motionOf, pollInterval, type Floor, type FloorState } from "./tower";

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

const withEps = (code: string, state: FloorState, n: number): Floor => ({ ...floor(code, state), episodes: Array.from({ length: n }, (_, i) => ({ id: i + 1, number: i + 1, title: "t" })) });
const live = { stale: false, reduced: false };
const tw = (fl: Floor[], f: string | null = null) => ({ floors: fl, active_episode: f ? ep(f) : null });

describe("motionOf", () => {
  it("pulses only the active floors' lamps and slides only crates on an active floor", () => {
    const fl = FLOOR_CODES.map((c) => (c === "2F" ? withEps(c, "active", 1) : c === "6F" ? withEps(c, "waiting", 1) : floor(c, c === "1F" ? "done" : "idle")));
    const m = motionOf(tw(fl, "2F"), live);
    expect(m.lamps).toEqual(["2F"]);
    expect(m.crates.map((c) => [c.code, c.moving])).toEqual([["6F", false], ["2F", true]]);
    expect(m.car).toEqual({ floor: "2F", travel: true });
  });
  it("draws a crate only for an episode the payload places on a conveyor floor", () => {
    expect(motionOf(tw(floors({ "2F": "active" }), "2F"), live).crates).toEqual([]);
    const m = motionOf(tw(FLOOR_CODES.map((c) => withEps(c, "idle", 1))), live);
    expect(m.crates.map((c) => c.code).sort()).toEqual(["1F", "2F", "3F", "4F", "5F", "6F"]);
    expect(m.crates.every((c) => !c.moving)).toBe(true);
  });
  it("never animates idle, done, failed, waiting or not_implemented floors", () => {
    const states: FloorState[] = ["idle", "done", "failed", "waiting", "not_implemented"];
    const m = motionOf(tw(FLOOR_CODES.map((c, i) => withEps(c, states[i % states.length], 1))), live);
    expect(m.lamps).toEqual([]);
    expect(m.crates.some((c) => c.moving)).toBe(false);
    expect(m.car.travel).toBe(false);
  });
  it("parks the car at G with no motion when nothing runs", () => {
    expect(motionOf(tw(floors()), live)).toMatchObject({ lamps: [], crates: [], car: { floor: "G", travel: false } });
  });
  it("stops all motion when disconnected or reduced motion is asked for, but keeps crates and car position", () => {
    const fl = FLOOR_CODES.map((c) => (c === "3F" ? withEps(c, "active", 2) : floor(c, "idle")));
    for (const o of [{ stale: true, reduced: false }, { stale: false, reduced: true }]) {
      const m = motionOf(tw(fl, "3F"), o);
      expect(m.enabled).toBe(false);
      expect(m.lamps).toEqual([]);
      expect(m.crates).toHaveLength(2);
      expect(m.crates.some((c) => c.moving)).toBe(false);
      expect(m.car).toEqual({ floor: "3F", travel: false });
    }
  });
});

describe("pollInterval", () => {
  const running = tw(floors({ "1F": "active" }));
  it("is 3 s while a run is active and 15 s when idle", () => {
    expect(pollInterval(running, { hidden: false, stale: false })).toBe(3000);
    expect(pollInterval({ floors: floors(), active_episode: ep("1F") }, { hidden: false, stale: false })).toBe(3000);
    expect(pollInterval(tw(floors({ "1F": "done" })), { hidden: false, stale: false })).toBe(15000);
  });
  it("pauses while hidden, whatever the state", () => {
    expect(pollInterval(running, { hidden: true, stale: false })).toBeNull();
    expect(pollInterval(null, { hidden: true, stale: true })).toBeNull();
  });
  it("retries every 5 s when the backend is unreachable and polls fast before the first payload", () => {
    expect(pollInterval(running, { hidden: false, stale: true })).toBe(5000);
    expect(pollInterval(null, { hidden: false, stale: false })).toBe(3000);
  });
});
