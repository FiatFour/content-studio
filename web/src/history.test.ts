import { describe, expect, it } from "vitest";
import { dictionaries } from "./i18n";
import { certaintyBadge, certaintyCounts, certaintyLabelKey, certaintyLevels } from "./history";

describe("history review helpers", () => {
  it("has a Thai and English label and a distinct mark for every certainty level", () => {
    const marks = new Set<string>();
    for (const c of certaintyLevels) {
      const key = certaintyLabelKey(c);
      expect(dictionaries.th[key], `th:${key}`).toBeTruthy();
      expect(dictionaries.en[key], `en:${key}`).toBeTruthy();
      marks.add(certaintyBadge(c).mark);
    }
    expect(marks.size).toBe(certaintyLevels.length);
  });

  it("shows an unrecognised certainty as unknown, not as documented", () => {
    expect(certaintyLabelKey("certain")).toBe("review.history.certainty.unknown");
    expect(certaintyBadge("certain").tone).toBe("muted");
  });

  it("only documented claims are green", () => {
    expect(certaintyBadge("documented").tone).toBe("text-green");
    expect(certaintyBadge("disputed").tone).not.toBe("text-green");
    expect(certaintyBadge("speculation").tone).not.toBe("text-green");
  });

  it("fills missing counts with zero", () => {
    expect(certaintyCounts({ documented: 2 })).toEqual({ documented: 2, disputed: 0, speculation: 0 });
  });

  it("states that the pictures are illustrations, not evidence, in both languages", () => {
    expect(dictionaries.en["review.history.illustration"]).toMatch(/illustrations, not evidence/);
    expect(dictionaries.th["review.history.illustration"]).toBeTruthy();
    expect(dictionaries.th["review.history.illustration"]).not.toBe(dictionaries.en["review.history.illustration"]);
  });
});
