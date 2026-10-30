import { describe, expect, it } from "vitest";
import { carCentreY, carFloor, FLOOR_CODES, floorBox, FLOOR_H, ROOF, visualOf, type Floor, type FloorState } from "./tower";

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
