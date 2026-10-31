import type { Key } from "./i18n";

// The profiles the new-episode form offers. Ranges mirror the Go profile specs (internal/pipeline/profile.go);
// the server enforces them. runnable is false for a profile whose stages do not exist: the form will not submit it.
export type ProfileId = "fictional_series" | "knowledge_shorts" | "history_mystery";
export const profiles: { id: ProfileId; min: number; max: number; runnable: boolean }[] = [
  { id: "fictional_series", min: 60, max: 120, runnable: true },
  { id: "knowledge_shorts", min: 30, max: 60, runnable: true },
  { id: "history_mystery", min: 60, max: 120, runnable: true },
];

// takesSources is true for a profile whose script is written from owner-pasted sources: its episode
// waits for a source, then the owner starts the run.
export function takesSources(id: string): boolean {
  return id === "knowledge_shorts" || id === "history_mystery";
}

export function profileLabel(id: string): Key {
  return (profiles.some((p) => p.id === id) ? `profile.${id}` : "profile.unknown") as Key;
}
