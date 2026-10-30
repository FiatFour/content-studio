import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { dictionaries, translate } from "./index";

describe("translations", () => {
  it("has the same keys in Thai and English", () => {
    const th = Object.keys(dictionaries.th).sort();
    const en = Object.keys(dictionaries.en).sort();
    expect(th.filter((k) => !en.includes(k))).toEqual([]);
    expect(en.filter((k) => !th.includes(k))).toEqual([]);
  });

  it("has no empty strings", () => {
    for (const lang of ["th", "en"] as const)
      for (const [k, v] of Object.entries(dictionaries[lang])) expect(v.trim(), `${lang}:${k}`).not.toBe("");
  });

  it("explains every job error class in both languages", () => {
    // Keep in step with jobs.AllErrorClasses; the Go test TestEveryErrorClassHasThaiAndEnglishExplanation checks the other direction.
    const classes = ["invalid_input", "auth_or_permission", "rate_limited", "provider_outage", "uncertain_outcome", "transient_network",
      "validation_failed", "unsupported", "insufficient_local_resources", "internal", "worker_restart", "qa_failed", "script_duration", "cancelled"];
    for (const lang of ["th", "en"] as const)
      for (const c of classes) for (const part of ["what", "next"]) expect(dictionaries[lang][`errclass.${c}.${part}`], `${lang}:${c}.${part}`).toBeTruthy();
  });

  it("fills placeholders", () => {
    expect(translate("en", "episodes.episode", { n: 3 })).toBe("Episode 3");
  });

  it("uses only keys that exist", () => {
    const keys = new Set(Object.keys(dictionaries.th));
    const used: string[] = [];
    const walk = (dir: string) => {
      for (const e of readdirSync(dir, { withFileTypes: true })) {
        const p = join(dir, e.name);
        if (e.isDirectory()) walk(p);
        else if (/\.tsx?$/.test(e.name) && !e.name.endsWith(".test.ts"))
          for (const m of readFileSync(p, "utf8").matchAll(/\bt\("([a-zA-Z0-9_.]+)"/g)) used.push(m[1]);
      }
    };
    walk(join(import.meta.dirname, ".."));
    expect(used.filter((k) => !keys.has(k))).toEqual([]);
  });

  it("has no hard-coded Thai in components", () => {
    const bad: string[] = [];
    const walk = (dir: string) => {
      for (const e of readdirSync(dir, { withFileTypes: true })) {
        const p = join(dir, e.name);
        if (e.isDirectory()) { if (e.name !== "i18n") walk(p); }
        else if (/\.tsx?$/.test(e.name) && /[฀-๿]/.test(readFileSync(p, "utf8"))) bad.push(p);
      }
    };
    walk(join(import.meta.dirname, ".."));
    expect(bad).toEqual([]);
  });
});
