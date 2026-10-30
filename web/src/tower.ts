// Pure helpers for the agent tower. State comes only from GET /api/tower (internal/tower decides
// floor states); this file only maps a state to how it is drawn and where things sit on the canvas.

export type FloorState = "idle" | "active" | "done" | "failed" | "waiting" | "not_implemented";
export type Progress = { done: number; total: number };
export type EpisodeRef = { id: number; number: number; title: string };
export type Floor = {
  code: string; agent: string; state: FloorState; episodes: EpisodeRef[];
  tasks_completed: number | null; handoff_from: string | null; handoff_to: string | null;
  provider: string | null; model: string | null; last_log: string | null; progress: Progress | null; note?: string;
};
export type StageStatus = { stage: string; agent: string; floor: string; state: string; progress: Progress | null };
export type EpisodeStatus = {
  run_id: number; run_state: string; floor: string | null; stages: Progress; current: Progress | null;
  pipeline?: StageStatus[]; id?: number; number?: number; title?: string;
};
export type TowerState = {
  floors: Floor[];
  active_episode: EpisodeStatus | null;
  today: EpisodeStatus[];
  awaiting_review: unknown;
  continuity: { available: boolean; note: string };
};
export type TowerPayload = { generated_at: string; tower: TowerState };

export const FLOOR_CODES = ["Roof", "6F", "5F", "4F", "3F", "2F", "1F", "G", "B1"] as const; // top to bottom

// Lamp on a floor sign and dot on a floor label. Shape differs per state so colour is never the only cue.
export type Lamp = "blue" | "green" | "grey" | "red" | "amber" | "hollow";
export type Glyph = "check" | "dot" | "ring" | "cross" | "pause" | "dash";
export type Visual = { lamp: Lamp; glyph: Glyph; color: string; dashed: boolean };

const VISUALS: Record<FloorState, Visual> = {
  done: { lamp: "blue", glyph: "check", color: "var(--color-blue)", dashed: false },
  active: { lamp: "green", glyph: "dot", color: "var(--color-green)", dashed: false },
  idle: { lamp: "grey", glyph: "ring", color: "var(--color-muted)", dashed: false },
  failed: { lamp: "red", glyph: "cross", color: "var(--color-badge-red)", dashed: false },
  waiting: { lamp: "amber", glyph: "pause", color: "var(--color-amber)", dashed: true },
  not_implemented: { lamp: "hollow", glyph: "dash", color: "var(--color-muted)", dashed: true },
};

// visualOf maps a floor state to its look. An unknown state (a newer backend) is treated as idle
// rather than guessed at; the caller still prints the raw word.
export function visualOf(state: string): Visual {
  return VISUALS[state as FloorState] ?? VISUALS.idle;
}

export const isKnownState = (s: string): s is FloorState => s in VISUALS;

// Canvas geometry (SVG user units, viewBox 1000 x 890). The floor buttons are positioned from the same numbers.
export const VIEW = { w: 1000, h: 890 };
export const ROOF = { y: 0, h: 150 };
export const FLOOR_H = 90;
export const floorBox = (code: string): { y: number; h: number } => {
  const i = FLOOR_CODES.indexOf(code as (typeof FLOOR_CODES)[number]);
  if (i < 0) return { y: 0, h: 0 };
  return i === 0 ? ROOF : { y: ROOF.h + (i - 1) * FLOOR_H, h: FLOOR_H };
};

export const CAR_H = 56;
// carCentreY: where the car's centre sits when parked at a floor.
export const carCentreY = (code: string): number => {
  const b = floorBox(code);
  return code === "Roof" ? b.y + b.h - 10 - CAR_H / 2 : b.y + b.h / 2;
};

// carFloor decides where the elevator car parks. Only real state counts: the floor of the active
// episode's crate, else a floor that is working or stuck. Null means nothing is running; the car then
// rests at the lobby (G) and the scene says so instead of implying a trip.
export function carFloor(t: Pick<TowerState, "floors" | "active_episode">): string | null {
  const known = (c: string | null | undefined): c is string => !!c && t.floors.some((f) => f.code === c);
  const crate = t.active_episode?.floor;
  if (known(crate)) return crate;
  const hot = t.floors.find((f) => f.state === "active") ?? t.floors.find((f) => f.state === "failed") ?? t.floors.find((f) => f.state === "waiting");
  return hot?.code ?? null;
}

// Agent colours (docs/spec/ui.md). The export drone has no colour in the spec and takes the muted token.
export const agentColour: Record<string, string> = {
  orchestrator: "var(--color-agent-orch)", script: "var(--color-agent-script)", art: "var(--color-agent-art)",
  voice: "var(--color-agent-voice)", render: "var(--color-agent-render)", qa: "var(--color-agent-qa)",
  review: "var(--color-agent-review)", guardrails: "var(--color-agent-guard)", export: "var(--color-muted)",
};
