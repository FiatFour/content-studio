// Drives the isolated studio UI in headless Chromium the way a user would, and writes evidence.
// usage: node drive.mjs <env file> <flow>      flows: shell | create-episode | notifications | resources | auth
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
    for (const [key, tag] of [["menu.tower", "menu.tag.m4"], ["menu.library", "menu.tag.m3"]]) {
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

  // Providers & budget: the Discord notifications card against real backend state. The webhook validator only
  // accepts discord.com URLs, so a local fake receiver cannot be used: an unconfigured instance proves the
  // "not configured" / 409 path, and an instance launched with a fake-but-valid URL proves the masked suffix
  // (its test button is NOT pressed, because that would call the real discord.com).
  async notifications(page) {
    await api("/settings", { method: "PUT", body: JSON.stringify({ ui_lang: "th" }) });
    const sql = (q) => execFileSync("docker", ["exec", "studio-pg", "psql", "-U", "studio", "-d", env.DB, "-tAc", q], { encoding: "utf8" }).trim();
    const before = (await api("/notifications")).body;
    await page.goto(`${env.URL}/#/providers`);
    await expectVisible("providers page heading", page.getByRole("heading", { name: t("th", "providers.title") }));
    await expectVisible("notifications card", page.getByRole("heading", { name: t("th", "notif.title") }));
    if (before.configured) {
      await expectVisible("configured with masked suffix", page.getByText(`${t("th", "notif.configured")} (\u2026${before.suffix})`, { exact: true }));
      const html = await page.content();
      check("no webhook URL anywhere in the page", !/discord\.com\/api\/webhooks|api\/webhooks/.test(html));
      check("API returns only a 4 character suffix", before.suffix.length === 4 && !JSON.stringify(before).includes("webhooks"));
    } else {
      await expectVisible("not configured is shown", page.getByText(t("th", "notif.notConfigured"), { exact: true }));
    }
    // Unimplemented events are disabled and labelled.
    const unimpl = before.events.filter((e) => !e.implemented);
    check("backend lists unimplemented events", unimpl.length > 0);
    for (const e of unimpl) check(`${e.event} checkbox disabled`, await page.getByLabel(t("th", `notif.event.${e.event}`)).isDisabled());
    check("Not implemented labels shown", (await page.getByText(t("th", "app.notImplemented"), { exact: true }).count()) >= unimpl.length);
    // Toggle an implemented event off and on through the UI; read it back from the backend and the audit log.
    const box = page.getByLabel(t("th", "notif.event.job_failed"));
    const was = await box.isChecked();
    await box.click();
    await page.waitForFunction(async ([w]) => (await (await fetch("/api/notifications")).json()).events.find((e) => e.event === "job_failed").enabled === !w, [was]);
    const after = (await api("/notifications")).body.events.find((e) => e.event === "job_failed").enabled;
    check("toggle persisted in the backend", after === !was, `was ${was}, now ${after}`);
    check("toggle audited", sql("select count(*) from audit_log where action = 'notification.toggle'") === "1");
    await page.screenshot({ path: `${evidence}/1-toggled.png`, fullPage: true });
    await box.click();
    await page.waitForFunction(async ([w]) => (await (await fetch("/api/notifications")).json()).events.find((e) => e.event === "job_failed").enabled === w, [was]);
    check("toggled back", (await api("/notifications")).body.events.find((e) => e.event === "job_failed").enabled === was);
    if (!before.configured) {
      const direct = await api("/notifications/test", { method: "POST", body: "{}" });
      check("backend answers 409 for the test message", direct.status === 409, String(direct.status));
      await page.getByRole("button", { name: t("th", "notif.test") }).click();
      await expectVisible("UI shows the not-configured result", page.getByText(t("th", "notif.testNotConfigured")));
      check("nothing recorded as delivered", sql("select count(*) from notification_deliveries") === "0");
    }
    await page.getByRole("button", { name: "EN", exact: true }).click();
    await expectVisible("English card title", page.getByRole("heading", { name: t("en", "notif.title") }));
  },

  // Providers & budget: resource limit and read-only task policies. Changing the GPU limit above 1 needs the
  // warning step; the stored limit and the audit row are read back from the instance's own database.
  async resources(page) {
    await api("/settings", { method: "PUT", body: JSON.stringify({ ui_lang: "en" }) });
    const sql = (q) => execFileSync("docker", ["exec", "studio-pg", "psql", "-U", "studio", "-d", env.DB, "-tAc", q], { encoding: "utf8" }).trim();
    await page.goto(`${env.URL}/#/providers`);
    await expectVisible("resource card", page.getByRole("heading", { name: t("en", "res.title") }));
    const input = page.getByLabel(t("en", "res.limit", { max: 4 }));
    check("limit input shows the stored value (1)", (await input.inputValue()) === sql("select max_concurrent from resource_limits where resource = 'gpu'"));
    // Policies: read-only, all LOCAL_ONLY, Not implemented to change.
    await expectVisible("policy table lists the script task", page.getByRole("cell", { name: "script", exact: true }));
    check("four LOCAL_ONLY rows", (await page.getByRole("cell", { name: "LOCAL_ONLY", exact: true }).count()) === 4);
    await expectVisible("cloud off is shown", page.getByText(t("en", "pol.cloudOff")));
    // Invalid value: Save is disabled and the reason is shown.
    await input.fill("0");
    await expectVisible("invalid value explained", page.getByText(t("en", "res.invalid", { max: 4 })));
    check("Save disabled for 0", await page.getByRole("button", { name: t("en", "res.save") }).isDisabled());
    check("nothing stored for the invalid value", sql("select max_concurrent from resource_limits where resource = 'gpu'") === "1");
    // Raising to 2 asks for confirmation first; cancel changes nothing.
    await input.fill("2");
    await page.getByRole("button", { name: t("en", "res.save") }).click();
    await expectVisible("unsafe warning shown", page.getByText(t("en", "res.warnTitle")));
    await page.screenshot({ path: `${evidence}/1-warning.png`, fullPage: true });
    check("nothing stored before confirming", sql("select max_concurrent from resource_limits where resource = 'gpu'") === "1");
    await page.getByRole("button", { name: t("en", "app.cancel") }).click();
    check("cancel keeps the limit at 1", sql("select max_concurrent from resource_limits where resource = 'gpu'") === "1");
    await page.getByRole("button", { name: t("en", "res.save") }).click();
    await page.getByRole("button", { name: t("en", "res.confirm", { n: 2 }) }).click();
    await expectVisible("saved message", page.getByText(t("en", "res.saved", { name: "gpu", n: 2 })));
    check("limit 2 persisted", sql("select max_concurrent from resource_limits where resource = 'gpu'") === "2");
    check("audit row with from 1 to 2 and unsafe confirmed", sql("select detail->>'from' || '>' || (detail->>'to') || ' ' || (detail->>'confirmed_unsafe') from audit_log where action = 'resource_limit.update'") === "1>2 true");
    await page.reload();
    check("a reload shows 2", (await page.getByLabel(t("en", "res.limit", { max: 4 })).inputValue()) === "2");
    // Back to 1 needs no confirmation.
    await page.getByLabel(t("en", "res.limit", { max: 4 })).fill("1");
    await page.getByRole("button", { name: t("en", "res.save") }).click();
    await expectVisible("lowered without a warning", page.getByText(t("en", "res.saved", { name: "gpu", n: 1 })));
    check("limit back at 1", sql("select max_concurrent from resource_limits where resource = 'gpu'") === "1");
    check("two audit rows", sql("select count(*) from audit_log where action = 'resource_limit.update'") === "2");
    await page.getByRole("button", { name: "TH", exact: true }).click();
    await expectVisible("Thai card title", page.getByRole("heading", { name: t("th", "res.title") }));
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

// Knowledge Shorts sources. No such episode can be created in the UI (the profile is not runnable), so the
// project and episode are seeded by SQL into the throwaway database. Then the user path: paste, save, add a version.
flows.sources = async function (page) {
  const sql = (q) => execFileSync("docker", ["exec", "studio-pg", "psql", "-U", "studio", "-d", env.DB, "-tAc", q], { encoding: "utf8" }).trim();
  const eid = sql("with p as (insert into projects (name, profile) values ('Knowledge Shorts', 'knowledge_shorts') returning id) insert into episodes (project_id, number, title, idea, target_duration_s) select id, 1, 'seeded ks episode', 'seeded idea text', 45 from p returning id").split("\n")[0];
  const fs = sql("select id from episodes where title <> 'seeded ks episode' limit 1");
  await api("/settings", { method: "PUT", body: JSON.stringify({ ui_lang: "en" }) });
  await page.goto(`${env.URL}/#/episodes/${eid}`);
  await expectVisible("Sources card is shown for knowledge_shorts", page.getByRole("heading", { name: t("en", "sources.title") }));
  await expectVisible("empty state", page.getByText(t("en", "sources.empty")));
  const evil = "<img src=x onerror=alert(1)> & <b>bold</b>";
  await page.getByLabel(t("en", "sources.fTitle")).fill("Water facts");
  await page.getByLabel(t("en", "sources.fCitation")).fill("Textbook p. 4 " + evil);
  await page.getByLabel(t("en", "sources.fText")).fill("Water boils at 100 C at sea level.\n" + evil);
  await page.getByRole("button", { name: t("en", "sources.add"), exact: true }).click();
  await expectVisible("saved message", page.getByText(t("en", "sources.saved", { id: "s1", n: 1 })));
  await expectVisible("list row s1", page.getByText("s1", { exact: true }));
  await page.getByRole("button", { name: t("en", "sources.view") }).click();
  await expectVisible("text shown as text (markup visible literally)", page.locator("pre", { hasText: "<img src=x onerror=alert(1)>" }));
  check("no img element was injected", (await page.locator("img[src='x']").count()) === 0);
  await page.getByRole("button", { name: t("en", "sources.newVersion") }).click();
  await expectVisible("form seeded for a new version", page.getByText(t("en", "sources.editing", { id: "s1" })));
  await page.getByLabel(t("en", "sources.fText")).fill("Water boils at 100 C at sea level. Revised.");
  await page.getByRole("button", { name: t("en", "sources.addVersion", { id: "s1" }) }).click();
  await expectVisible("version 2 saved", page.getByText(t("en", "sources.saved", { id: "s1", n: 2 })));
  const list = await api(`/episodes/${eid}/sources`);
  const s1 = list.body?.sources?.[0];
  check("backend: one source with 2 versions, latest v2", list.body?.sources?.length === 1 && s1?.version === 2 && s1?.versions?.length === 2, JSON.stringify({ n: list.body?.sources?.length, v: s1?.version }));
  check("backend: audit rows source.create + source.version", sql(`select string_agg(action, ',' order by id) from audit_log where action like 'source.%' and detail->>'episode_id' = '${eid}'`) === "source.create,source.version");
  check("backend: audit detail has no text", !sql("select string_agg(detail::text, ' ') from audit_log where action like 'source.%'").includes("Water"));
  await page.screenshot({ path: `${evidence}/sources.png`, fullPage: true });
  if (fs) {
    await page.goto(`${env.URL}/#/episodes/${fs}`);
    await page.waitForTimeout(1500);
    check("no Sources card on a fictional_series episode", (await page.getByRole("heading", { name: t("en", "sources.title") }).count()) === 0);
  }
  await api("/settings", { method: "PUT", body: JSON.stringify({ ui_lang: "th" }) });
  await page.goto(`${env.URL}/#/episodes/${eid}`);
  await page.reload();
  await expectVisible("Thai title", page.getByRole("heading", { name: t("th", "sources.title") }));
  await page.screenshot({ path: `${evidence}/sources-th.png`, fullPage: true });
};

// Knowledge Shorts through the UI only: create the episode (no run yet), see the pending state, try to start
// with no source (refused, message shown), paste a source, start, and read the stored run. No worker runs here,
// so the script job stays queued; nothing here proves the real models work.
flows["shorts-episode"] = async (page) => {
  await api("/settings", { method: "PUT", body: JSON.stringify({ ui_lang: "en" }) });
  await page.goto(`${env.URL}/#/episodes`);
  const title = `shorts ${env.ID}`;
  await page.getByLabel(t("en", "episodes.form.profile")).selectOption("knowledge_shorts");
  check("Knowledge Shorts option carries no Not-implemented mark", (await page.getByRole("option", { name: t("en", "profile.knowledge_shorts"), exact: true }).count()) === 1);
  await expectVisible("Shorts hint about sources", page.getByText(t("en", "episodes.form.shortsHint"), { exact: true }));
  check("submit is enabled", await page.getByRole("button", { name: t("en", "episodes.form.submit") }).isEnabled());
  await page.getByLabel(t("en", "episodes.form.title")).fill(title);
  await page.getByLabel(t("en", "episodes.form.idea")).fill("ทำไมน้ำทะเลถึงเค็ม");
  await page.getByLabel(t("en", "episodes.form.length")).fill("45");
  await page.screenshot({ path: `${evidence}/form-en.png`, fullPage: true });
  await page.getByRole("button", { name: t("en", "episodes.form.submit") }).click();
  await page.waitForURL(/#\/episodes\/\d+$/, { timeout: 8000 });
  const id = Number(page.url().split("/").pop());
  await expectVisible("pending state card", page.getByRole("heading", { name: t("en", "episode.start.title") }));
  let ep = (await api(`/episodes/${id}`)).body;
  check("backend: shorts episode with no run", ep?.episode?.profile === "knowledge_shorts" && ep.runs.length === 0, JSON.stringify(ep?.runs));
  await page.screenshot({ path: `${evidence}/pending-en.png`, fullPage: true });
  await page.getByRole("button", { name: t("en", "episode.start.button") }).click();
  await expectVisible("refusal asks for a source", page.getByRole("alert").filter({ hasText: "at least one source" }));
  ep = (await api(`/episodes/${id}`)).body;
  check("backend: still no run after the refused start", ep.runs.length === 0);
  await page.getByLabel(t("en", "sources.fTitle")).fill("Sea water facts");
  await page.getByLabel(t("en", "sources.fText")).fill("น้ำทะเลมีความเค็มเฉลี่ยประมาณ 35 กรัมต่อลิตร");
  await page.getByRole("button", { name: t("en", "sources.add"), exact: true }).click();
  await expectVisible("source listed", page.getByText("Sea water facts").first());
  await page.getByRole("button", { name: t("en", "episode.start.button") }).click();
  await expectVisible("stages strip shows the script stage", page.getByText(t("en", "stage.script"), { exact: true }));
  await expectVisible("script stage queued", page.getByText(t("en", "state.queued"), { exact: true }));
  ep = (await api(`/episodes/${id}`)).body;
  const run = ep.runs[0];
  check("backend: one full run with the six Shorts stages and a queued script job", ep.runs.length === 1 && run.kind === "full" && run.stages.join() === "script,tts,images,subtitles,render,qa" && run.jobs[0]?.stage === "script" && run.jobs[0].state === "queued", JSON.stringify(run?.stages));
  await page.screenshot({ path: `${evidence}/queued-en.png`, fullPage: true });
  await api("/settings", { method: "PUT", body: JSON.stringify({ ui_lang: "th" }) });
  await page.reload();
  await expectVisible("Thai stage label", page.getByText(t("th", "stage.script"), { exact: true }));
  await page.screenshot({ path: `${evidence}/queued-th.png`, fullPage: true });
  await page.goto(`${env.URL}/#/episodes`);
  await page.reload();
  await page.getByLabel(t("th", "episodes.form.profile")).selectOption("knowledge_shorts");
  await expectVisible("Thai shorts hint", page.getByText(t("th", "episodes.form.shortsHint"), { exact: true }));
  await page.screenshot({ path: `${evidence}/form-th.png`, fullPage: true });
};

if (!flows[flow]) { console.error(`unknown flow ${flow}; choose ${Object.keys(flows).join(" | ")}`); process.exit(2); }
const browser = await chromium.launch();
const context = await browser.newContext({ viewport: { width: 1280, height: 900 } });
// The browser holds the test session cookie, as a logged-in owner's would be.
if (env.SESSION && flow !== "auth") await context.addCookies([{ name: "studio_session", value: env.SESSION, url: env.URL, httpOnly: true, sameSite: "Strict" }]);
const page = await context.newPage();
const consoleErrors = [];
page.on("pageerror", (e) => consoleErrors.push(String(e).slice(0, 200)));
// The server sends a strict Content-Security-Policy; a violation shows up as a console error. Any one fails the run.
const cspViolations = [];
page.on("console", (m) => { if (/Content Security Policy|Permissions-Policy|Refused to (load|apply|execute|connect|frame|create)/i.test(m.text())) cspViolations.push(m.text().slice(0, 200)); });
try { await flows[flow](page, context); } catch (e) { check("flow ran to the end", false, String(e).slice(0, 300)); }
check("no Content-Security-Policy / Permissions-Policy problems in the console", cspViolations.length === 0, cspViolations.join(" | "));
await page.screenshot({ path: `${evidence}/screenshot.png`, fullPage: true });
writeFileSync(`${evidence}/aria.txt`, await page.locator("body").ariaSnapshot());
check("no uncaught page errors", consoleErrors.length === 0, consoleErrors.join(" | "));
writeFileSync(`${evidence}/result.json`, JSON.stringify({ flow, url: env.URL, checks: log }, null, 2));
await browser.close();
process.exit(log.every((c) => c.ok) ? 0 : 1);
