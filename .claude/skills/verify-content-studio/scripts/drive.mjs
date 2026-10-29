// Drives the isolated studio UI in headless Chromium the way a user would, and writes evidence.
// usage: node drive.mjs <env file> <flow>      flows: shell | create-episode | auth
// Every visible string comes from web/src/i18n/{th,en}.json, so a renamed label breaks the flow
// loudly instead of silently passing. Exit code 0 only if every assertion held.
import { createRequire } from "node:module";
import { readFileSync, mkdirSync, writeFileSync } from "node:fs";
import { execFileSync } from "node:child_process";
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

// API calls carry the test session cookie and an allowed Origin (state-changing requests need one).
const apiOrigin = `http://localhost:${env.PORT}`;
const api = (path, init = {}) => fetch(`${env.URL}/api${path}`, { ...init, headers: { "Content-Type": "application/json", Origin: apiOrigin, Cookie: `studio_session=${env.SESSION}`, ...init.headers } }).then(async (r) => ({ status: r.status, body: await r.json().catch(() => null) }));
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

// Passkey sign-in end to end with a CDP virtual authenticator (no real device). Runs WITHOUT the test
// session: the browser starts logged out. The enrollment code is created with `server enroll` against this
// instance's throwaway database and only ever held in memory (never written to the evidence files).
flows.auth = async function (page, context) {
  const base = `http://localhost:${env.PORT}`; // WebAuthn needs a domain name, so localhost, not 127.0.0.1
  const cdp = await context.newCDPSession(page);
  await cdp.send("WebAuthn.enable");
  const { authenticatorId } = await cdp.send("WebAuthn.addVirtualAuthenticator", { options: { protocol: "ctap2", transport: "internal", hasResidentKey: true, hasUserVerification: true, isUserVerified: true, automaticPresenceSimulation: true } });
  const fetchStatus = (path, init) => page.evaluate(async ([p, i]) => { const r = await fetch(`/api${p}`, i); return { status: r.status, body: await r.json().catch(() => null) }; }, [path, init]);
  const shot = (name) => page.screenshot({ path: `${evidence}/${name}.png`, fullPage: true });
  const nav = () => page.getByRole("navigation", { name: "menu" });
  const sql = (q) => execFileSync("docker", ["exec", "studio-pg", "psql", "-U", "studio", "-d", env.DB, "-tAc", q], { encoding: "utf8" }).trim();

  await page.goto(base);
  await expectVisible("logged out: enrollment screen is shown", page.getByRole("heading", { name: t("th", "auth.enrollTitle") }));
  check("logged out: the API refuses without a session", (await fetchStatus("/episodes")).status === 401);
  check("logged out: no app shell rendered", (await nav().count()) === 0);
  await shot("1-enroll-screen");

  // A wrong code is refused with the fixed message and creates nothing.
  await page.getByLabel(t("th", "auth.code")).fill("AAAAA-BBBBB-CCCCC-DDDDD");
  await page.getByRole("button", { name: t("th", "auth.enrollBtn") }).click();
  await expectVisible("wrong code shows the failure message", page.getByText(t("th", "auth.err.failed")));
  check("wrong code stored no credential", sql("select count(*) from auth_credentials") === "0");

  const out = execFileSync(`${root}/${env.DIR}/server`, ["enroll"], { encoding: "utf8", env: { ...process.env, STUDIO_DATABASE_URL: `postgres://studio:studio@127.0.0.1:5433/${env.DB}`, STUDIO_ADDR: `127.0.0.1:${env.PORT}` } });
  const code = out.match(/[A-Z2-7]{5}(-[A-Z2-7]{5}){3}/)?.[0];
  check("server enroll printed a code to the terminal", !!code);
  check("the code is not stored in plaintext", !sql("select * from auth_enroll_codes").includes(code.replaceAll("-", "")));
  await page.getByLabel(t("th", "auth.code")).fill(code);
  await page.getByLabel(t("th", "auth.label")).fill("verify passkey");
  await page.getByRole("button", { name: t("th", "auth.enrollBtn") }).click();
  await expectVisible("enrollment signs in: app shell shown", nav().getByRole("link", { name: t("th", "menu.home") }));
  check("backend has exactly one credential", sql("select count(*) from auth_credentials") === "1");
  check("virtual authenticator holds the passkey", (await cdp.send("WebAuthn.getCredentials", { authenticatorId })).credentials.length === 1);
  const cookie = (await context.cookies()).find((c) => c.name === "studio_session");
  check("session cookie is HttpOnly + SameSite=Strict", cookie?.httpOnly === true && cookie?.sameSite === "Strict", JSON.stringify({ httpOnly: cookie?.httpOnly, sameSite: cookie?.sameSite, secure: cookie?.secure }));
  check("page scripts cannot read the cookie", !(await page.evaluate(() => document.cookie)).includes("studio_session"));
  check("the code cannot be used again", sql("select count(*) from auth_enroll_codes where used_at is not null") === "1");
  await shot("2-signed-in-after-enroll");
  await page.reload();
  await expectVisible("a reload keeps the session", nav().getByRole("link", { name: t("th", "menu.home") }));

  // Sign out, then sign in again with the passkey.
  await page.getByRole("button", { name: t("th", "auth.logout") }).click();
  await expectVisible("sign out: login screen with the passkey button", page.getByRole("button", { name: t("th", "auth.loginBtn") }));
  check("after sign out the API refuses", (await fetchStatus("/episodes")).status === 401);
  check("sign out revoked the session in the database", sql("select count(*) from auth_sessions where revoked_at is not null") === "1");
  await shot("3-signed-out");
  await page.getByRole("button", { name: t("th", "auth.loginBtn") }).click();
  await expectVisible("passkey sign in: app shell shown", nav().getByRole("link", { name: t("th", "menu.home") }));
  check("backend recorded a login", sql("select count(*) from audit_log where action = 'auth.login.success'") === "1");

  // Security page. The same authenticator is refused (it already holds a passkey for this app), then a
  // second authenticator, standing in for a second device, adds one. Then remove one; the last stays.
  await page.goto(`${base}/#/security`);
  await page.getByLabel(t("th", "auth.label")).fill("duplicate attempt");
  await page.getByRole("button", { name: t("th", "security.addBtn") }).click();
  await expectVisible("same authenticator is refused with the duplicate message", page.getByText(t("th", "auth.err.duplicate")));
  check("duplicate attempt stored nothing", sql("select count(*) from auth_credentials") === "1");
  await cdp.send("WebAuthn.removeVirtualAuthenticator", { authenticatorId }); // Chromium allows one internal authenticator at a time
  await cdp.send("WebAuthn.addVirtualAuthenticator", { options: { protocol: "ctap2", transport: "usb", hasResidentKey: true, hasUserVerification: true, isUserVerified: true, automaticPresenceSimulation: true } });
  await page.getByLabel(t("th", "auth.label")).fill("second passkey");
  await page.getByRole("button", { name: t("th", "security.addBtn") }).click();
  await expectVisible("second passkey listed", page.getByText("second passkey"));
  check("backend has two credentials", sql("select count(*) from auth_credentials") === "2");
  await shot("4-security-two-passkeys");
  await page.getByRole("button", { name: t("th", "security.remove") }).last().click();
  await page.getByRole("button", { name: t("th", "security.removeConfirm") }).click();
  await expectVisible("removal confirmed in the UI", page.getByText(t("th", "security.removed")));
  check("backend is back to one credential", sql("select count(*) from auth_credentials") === "1");
  await expectVisible("last passkey shows why it cannot be removed", page.getByText(t("th", "security.lastOneHint")));
  check("no remove button for the last passkey", (await page.getByRole("button", { name: t("th", "security.remove") }).count()) === 0);
  const lastId = sql("select id from auth_credentials");
  check("the API refuses removing the last passkey too", (await fetchStatus(`/auth/credentials/${lastId}`, { method: "DELETE" })).status === 409);

  // A session revoked on the server drops the running app back to the login screen (401 handling).
  sql("update auth_sessions set revoked_at = now() where revoked_at is null");
  await expectVisible("revoked session: login screen with the expired notice", page.getByText(t("th", "auth.expired")));
  await shot("5-session-expired");
};

if (!flows[flow]) { console.error(`unknown flow ${flow}; choose ${Object.keys(flows).join(" | ")}`); process.exit(2); }
const browser = await chromium.launch();
const context = await browser.newContext({ viewport: { width: 1280, height: 900 } });
// The browser holds the test session cookie, as a logged-in owner's would be.
if (env.SESSION && flow !== "auth") await context.addCookies([{ name: "studio_session", value: env.SESSION, url: env.URL, httpOnly: true, sameSite: "Strict" }]);
const page = await context.newPage();
const consoleErrors = [];
page.on("pageerror", (e) => consoleErrors.push(String(e).slice(0, 200)));
try { await flows[flow](page, context); } catch (e) { check("flow ran to the end", false, String(e).slice(0, 300)); }
await page.screenshot({ path: `${evidence}/screenshot.png`, fullPage: true });
writeFileSync(`${evidence}/aria.txt`, await page.locator("body").ariaSnapshot());
check("no uncaught page errors", consoleErrors.length === 0, consoleErrors.join(" | "));
writeFileSync(`${evidence}/result.json`, JSON.stringify({ flow, url: env.URL, checks: log }, null, 2));
await browser.close();
process.exit(log.every((c) => c.ok) ? 0 : 1);
