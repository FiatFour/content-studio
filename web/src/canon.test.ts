import { describe, expect, it } from "vitest";
import { cannotCheckReason, findingKind, groupFindings, prettyPayload, reasonValid, type Finding } from "./canon";

const f = (severity: Finding["severity"], message: string, code = "x"): Finding => ({ code, severity, message });

describe("canon helpers", () => {
  it("tells cannot-check info apart from plain info", () => {
    expect(findingKind(f("info", "cannot check: no birth data"))).toBe("cannot");
    expect(findingKind(f("info", "this proposal is already approved"))).toBe("info");
    expect(findingKind(f("conflict", "cannot check: odd"))).toBe("conflict");
    expect(cannotCheckReason("cannot check: no birth data")).toBe("no birth data");
  });

  it("groups conflicts first and drops empty groups", () => {
    const g = groupFindings([f("info", "i"), f("warning", "w"), f("info", "cannot check: c"), f("conflict", "k")]);
    expect(g.map((x) => x.kind)).toEqual(["conflict", "warning", "cannot", "info"]);
    expect(groupFindings([f("warning", "w")]).map((x) => x.kind)).toEqual(["warning"]);
    expect(groupFindings([])).toEqual([]);
  });

  it("validates a decision reason as 1 to 500 characters after trimming", () => {
    expect(reasonValid("")).toBe(false);
    expect(reasonValid("   ")).toBe(false);
    expect(reasonValid(" ok ")).toBe(true);
    expect(reasonValid("\u0e01".repeat(500))).toBe(true);
    expect(reasonValid("\u0e01".repeat(501))).toBe(false);
    expect(reasonValid("😀".repeat(500))).toBe(true);
  });

  it("pretty prints a payload as text", () => {
    expect(prettyPayload({ a: "<b>x</b>" })).toBe('{\n  "a": "<b>x</b>"\n}');
    expect(prettyPayload(undefined)).toBe("");
  });
});
