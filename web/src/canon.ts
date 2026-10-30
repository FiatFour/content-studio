// Pure helpers for the canon proposals page. Kept free of React so they can be unit tested.

export type FindingSeverity = "conflict" | "warning" | "info";
export type Finding = { code: string; severity: FindingSeverity; message: string };
// "cannot" is an info finding the checker could not evaluate. It is shown separately so it is never read as "passed".
export type FindingKind = "conflict" | "warning" | "cannot" | "info";

const CANNOT = "cannot check:";

export function findingKind(f: Finding): FindingKind {
  if (f.severity === "info" && f.message.startsWith(CANNOT)) return "cannot";
  if (f.severity === "conflict" || f.severity === "warning") return f.severity;
  return "info";
}

// The server wrote "cannot check: <why>"; the page supplies the translated label, so show only the reason.
export function cannotCheckReason(message: string): string {
  return message.startsWith(CANNOT) ? message.slice(CANNOT.length).trim() : message;
}

const order: FindingKind[] = ["conflict", "warning", "cannot", "info"];

// groupFindings returns the non-empty groups in display order (conflicts first), keeping the server order inside a group.
export function groupFindings(findings: Finding[]): { kind: FindingKind; items: Finding[] }[] {
  return order.map((kind) => ({ kind, items: findings.filter((f) => findingKind(f) === kind) })).filter((g) => g.items.length > 0);
}

export const REASON_MAX = 500;

// reasonLength counts characters the way the server does (runes), after trimming.
export function reasonLength(s: string): number {
  return [...s.trim()].length;
}

export function reasonValid(s: string): boolean {
  const n = reasonLength(s);
  return n >= 1 && n <= REASON_MAX;
}

// prettyPayload renders an untrusted payload as indented JSON text (the page shows it as text, never HTML).
export function prettyPayload(raw: unknown): string {
  try { return JSON.stringify(raw, null, 2) ?? ""; } catch { return ""; }
}
