import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { parseHash } from "./route";

const read = (p: string) => readFileSync(new URL(p, import.meta.url), "utf8");

describe("parseHash", () => {
  it("splits segments", () => {
    expect(parseHash("")).toEqual([]);
    expect(parseHash("#/")).toEqual([]);
    expect(parseHash("#/tower")).toEqual(["tower"]);
    expect(parseHash("#/episodes/12")).toEqual(["episodes", "12"]);
    expect(parseHash("#tower//x/")).toEqual(["tower", "x"]);
  });
});

describe("tower stays optional", () => {
  const app = read("./App.tsx");
  it("App never imports tower code statically", () => {
    expect(app).not.toMatch(/^import[^\n]*(pages\/Tower|TowerScene|TowerPanels|\.\/tower)/m);
    expect(app).toMatch(/lazy\(\(\) => import\("\.\/pages\/Tower"\)/);
  });
  it("only the tower page imports the tower stylesheet", () => {
    expect(read("./pages/Tower.tsx")).toMatch(/import "\.\.\/tower\.css"/);
    expect(read("./index.css")).not.toMatch(/tower-/);
  });
  it("the shell never polls /tower", () => {
    expect(app).not.toMatch(/usePoll[^\n]*"\/tower"/);
  });
});
