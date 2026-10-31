import type { Key } from "./i18n";

// Certainty levels of a History & Mystery claim (decision D19). Anything else the server sends is
// shown as "unknown" rather than guessed at.
export const certaintyLevels = ["documented", "disputed", "speculation"] as const;
export type Certainty = (typeof certaintyLevels)[number];

export function isCertainty(c: string): c is Certainty {
  return (certaintyLevels as readonly string[]).includes(c);
}

export function certaintyLabelKey(c: string): Key {
  return (isCertainty(c) ? `review.history.certainty.${c}` : "review.history.certainty.unknown") as Key;
}

// The badge never relies on colour alone: each level has its own mark as well as a word.
export function certaintyBadge(c: string): { mark: string; tone: string } {
  switch (c) {
    case "documented": return { mark: "●", tone: "text-green" };
    case "disputed": return { mark: "◐", tone: "text-amber" };
    case "speculation": return { mark: "○", tone: "text-red" };
    default: return { mark: "?", tone: "muted" };
  }
}

export function certaintyCounts(counts: Record<string, number>): { documented: number; disputed: number; speculation: number } {
  return { documented: counts.documented ?? 0, disputed: counts.disputed ?? 0, speculation: counts.speculation ?? 0 };
}
