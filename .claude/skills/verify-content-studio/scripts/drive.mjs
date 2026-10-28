// Drives the isolated studio UI in headless Chromium the way a user would, and writes evidence.
// usage: node drive.mjs <env file> <flow>      flows: shell | create-episode
// Every visible string comes from web/src/i18n/{th,en}.json, so a renamed label breaks the flow
// loudly instead of silently passing. Exit code 0 only if every assertion held.
import { createRequire } from "node:module";
import { readFileSync, mkdirSync, writeFileSync } from "node:fs";
import { resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../../../..");
process.env.LD_LIBRARY_PATH = `${root}/var/verify/tools/libs/usr/lib/x86_64-linux-gnu:${process.env.LD_LIBRARY_PATH ?? ""}`;
const { chromium } = createRequire(`${root}/var/verify/tools/`)("playwright");

const [envFile, flow] = process.argv.slice(2);
const env = Object.fromEntries(readFileSync(envFile, "utf8").trim().split("\n").map((l) => l.split(/=(.*)/s).slice(0, 2)));
const dict = { th: JSON.parse(readFileSync(`${root}/web/src/i18n/th.json`, "utf8")), en: JSON.parse(readFileSync(`${root}/web/src/i18n/en.json`, "utf8")) };
const t = (lang, key, vars = {}) => dict[lang][key].replace(/\{(\w+)\}/g, (_, k) => vars[k]);
const evidence = `${root}/${env.DIR}/evidence/${flow}`;
mkdirSync(evidence, { recursive: true });

const api = (path, init = {}) => fetch(`${env.URL}/api${path}`, { ...init, headers: { "Content-Type": "application/json", ...init.headers } }).then(async (r) => ({ status: r.status, body: await r.json().catch(() => null) }));
const log = [];
const check = (name, ok, detail = "") => { log.push({ name, ok: !!ok, detail }); console.log(`${ok ? "PASS" : "FAIL"} ${name} ${detail}`); };
const expectVisible = async (name, locator) => { try { await locator.first().waitFor({ state: "visible", timeout: 8000 }); check(name, true); } catch { check(name, false, "not visible within 8 s"); } };

const flows = {
  // Thai is the default; the nav lists live pages as links and later milestones as inert tagged items.
  async shell(page) {
    await api("/settings", { method: "PUT", body: JSON.stringify({ ui_lang: "th" }) });
    await page.goto(env.URL);
    await expectVisible("Thai nav link", page.getByRole("navigation", { name: "menu" }).getByRole("link", { name: t("th", "menu.episodes") }));
    for (const [key, tag] of [["menu.tower", "menu.tag.m4"], ["menu.providers", "menu.tag.m2"], ["menu.library", "menu.tag.m3"]]) {
      const nav = page.getByRole("navigation", { name: "menu" });
      check(`${key} is not a link`, (await nav.getByRole("link", { name: t("th", key) }).count()) === 0);
      const label = `${t("th", tag)} · ${t("th", "app.notImplemented")}`;
      check(`${key} is tagged "${label}"`, (await nav.getByText(label, { exact: true }).count()) > 0);
    }
    await page.getByRole("button", { name: "EN", exact: true }).click();
    await expectVisible("English nav link after switching", page.getByRole("navigation", { name: "menu" }).getByRole("link", { name: t("en", "menu.episodes") }));
    await page.reload();
    await expectVisible("English survives a reload", page.getByRole("navigation", { name: "menu" }).getByRole("link", { name: t("en", "menu.episodes") }));
    const s = await api("/settings");
    check("language stored in the backend", s.body?.ui_lang === "en", JSON.stringify(s.body));
  },

  // The user path: fill the form, submit, land on the episode page, and see a queued first stage.
  // No worker runs in this instance, so "queued" is the true and stable end state.
  async "create-episode"(page) {
    await api("/settings", { method: "PUT", body: JSON.stringify({ ui_lang: "en" }) });
    await page.goto(`${env.URL}/#/episodes`);
    const title = `verify ${env.ID}`;
    await page.getByLabel(t("en", "episodes.form.title")).fill(title);
    await page.getByLabel(t("en", "episodes.form.idea")).fill("คุณยายหัดใช้สมาร์ทโฟนครั้งแรก");
    await page.getByLabel(t("en", "episodes.form.length")).fill("60");
    await page.getByRole("button", { name: t("en", "episodes.form.submit") }).click();
    try { await page.waitForURL(/#\/episodes\/\d+$/, { timeout: 8000 }); check("lands on the episode page", true, page.url()); } catch { check("lands on the episode page", false, page.url()); }
    await expectVisible("episode page shows the first stage", page.getByText(t("en", "stage.script"), { exact: true }));
    await expectVisible("first stage is queued", page.getByText(t("en", "state.queued"), { exact: true }));
    const list = await api("/episodes");
    const ep = list.body?.episodes?.find((e) => e.title === title);
    check("backend row exists with the typed title", !!ep);
    check("backend run is running with a queued script job", ep?.latest_run?.state === "running" && ep.latest_run.jobs?.[0]?.stage === "script" && ep.latest_run.jobs[0].state === "queued", JSON.stringify(ep?.latest_run?.jobs?.[0]));
    await page.goto(`${env.URL}/#/episodes`);
    await expectVisible("episode listed after reload", page.getByRole("link", { name: new RegExp(title) }));
  },
};

if (!flows[flow]) { console.error(`unknown flow ${flow}; choose ${Object.keys(flows).join(" | ")}`); process.exit(2); }
const browser = await chromium.launch();
const page = await (await browser.newContext({ viewport: { width: 1280, height: 900 } })).newPage();
const consoleErrors = [];
page.on("pageerror", (e) => consoleErrors.push(String(e).slice(0, 200)));
try { await flows[flow](page); } catch (e) { check("flow ran to the end", false, String(e).slice(0, 300)); }
await page.screenshot({ path: `${evidence}/screenshot.png`, fullPage: true });
writeFileSync(`${evidence}/aria.txt`, await page.locator("body").ariaSnapshot());
check("no uncaught page errors", consoleErrors.length === 0, consoleErrors.join(" | "));
writeFileSync(`${evidence}/result.json`, JSON.stringify({ flow, url: env.URL, checks: log }, null, 2));
await browser.close();
process.exit(log.every((c) => c.ok) ? 0 : 1);
