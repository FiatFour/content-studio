// parseHash turns location.hash into route segments: "#/tower" -> ["tower"], "" and "#/" -> [].
export function parseHash(h: string): string[] {
  return h.replace(/^#\/?/, "").split("/").filter(Boolean);
}
