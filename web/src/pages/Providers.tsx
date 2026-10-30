import { useState } from "react";
import { api, ApiError, type Notifications, type NotifTestResult } from "../api";
import { usePoll } from "../hooks";
import { dictionaries, useI18n, type Key } from "../i18n";
import { Card, fmtTime } from "../ui";

// Providers & budget (M2). Only the Discord notifications card is real so far; the budget and
// provider parts are not built, and say so instead of showing placeholders that look live.
export function Providers() {
  const { t } = useI18n();
  return <div className="flex flex-col gap-4">
    <Card><h1 className="font-display text-2xl font-extrabold">{t("providers.title")}</h1>
      <p className="muted">{t("providers.localOnly")}</p></Card>
    <Card title={t("providers.budget")}><p className="muted">{t("app.notImplemented")} · {t("providers.budgetHelp")}</p></Card>
    <NotificationsCard />
  </div>;
}

type TestState = { kind: "result"; r: NotifTestResult } | { kind: "notConfigured" } | { kind: "failed" };

function NotificationsCard() {
  const { t, lang } = useI18n();
  const n = usePoll<Notifications>("/notifications", 10000);
  const [busy, setBusy] = useState<string | null>(null);
  const [toggleErr, setToggleErr] = useState<string | null>(null);
  const [testing, setTesting] = useState(false);
  const [test, setTest] = useState<TestState | null>(null);
  const d = n.data;

  const evLabel = (e: string) => { const k = `notif.event.${e}`; return k in dictionaries[lang] ? t(k as Key) : e; };

  const toggle = async (event: string, enabled: boolean) => {
    setBusy(event); setToggleErr(null);
    try { await api(`/notifications/events/${encodeURIComponent(event)}`, { method: "PUT", body: { enabled } }); n.reload(); }
    catch { setToggleErr(event); } finally { setBusy(null); }
  };
  const sendTest = async () => {
    setTesting(true); setTest(null);
    try { setTest({ kind: "result", r: await api<NotifTestResult>("/notifications/test", { method: "POST", body: {} }) }); n.reload(); }
    catch (e) { setTest(e instanceof ApiError && e.status === 409 ? { kind: "notConfigured" } : { kind: "failed" }); } finally { setTesting(false); }
  };
  const testOk = test?.kind === "result" && test.r.ok;

  return <Card title={t("notif.title")}>
    <p className="muted mb-3">{t("notif.help")}</p>
    {!d ? <p className={n.error ? "text-red" : "muted"} role={n.error ? "alert" : undefined}>{n.error ? t("notif.loadFailed") : t("app.loading")}</p> : <div className="flex flex-col gap-4">
      <div className="inner flex flex-wrap items-center gap-3">
        <span className={`mono inline-flex items-center gap-2 ${d.configured ? "text-green" : "text-amber"}`}>
          <span aria-hidden className="h-2.5 w-2.5 rounded-full bg-current" />
          {d.configured ? `${t("notif.configured")} (…${d.suffix ?? ""})` : t("notif.notConfigured")}</span>
        {!d.configured && <span className="muted text-sm">{t("notif.notConfiguredHelp")}</span>}
        {d.last_delivery && <span className="muted mono text-sm">{t("notif.last")}: {d.last_delivery.ok ? t("notif.ok") : t("notif.failed")} · {fmtTime(d.last_delivery.at, lang)}</span>}
      </div>

      <div><h3 className="mb-2 font-semibold">{t("notif.events")}</h3>
        <ul className="flex flex-col gap-2">
          {d.events.map((e) => <li key={e.event} className="inner flex flex-wrap items-center gap-3">
            <input id={`ev-${e.event}`} type="checkbox" className="h-6 w-6" checked={e.enabled} disabled={!e.implemented || busy === e.event}
              onChange={(x) => void toggle(e.event, x.target.checked)} />
            <label htmlFor={`ev-${e.event}`} className="min-h-11 flex-1 content-center">{evLabel(e.event)}
              <span className="muted mono ml-2 text-sm">{e.default ? t("notif.defaultOn") : t("notif.defaultOff")}</span></label>
            {!e.implemented && <span className="mono rounded border border-line px-1.5 text-sm text-muted">{t("app.notImplemented")}</span>}
            {toggleErr === e.event && <span role="alert" className="text-sm text-red">{t("notif.toggleFailed")}</span>}
          </li>)}
        </ul>
        <p className="muted mt-2 text-sm">{t("notif.unimplementedHelp")}</p>
      </div>

      <div className="flex flex-wrap items-center gap-3">
        <button className="btn btn-primary" disabled={testing} onClick={() => void sendTest()}>{testing ? t("notif.testing") : t("notif.test")}</button>
        <span className="muted text-sm">{t("notif.testHelp")}</span>
      </div>
      {test && <p role={testOk ? "status" : "alert"} className={testOk ? "text-green" : "text-red"}>
        {test.kind === "notConfigured" ? t("notif.testNotConfigured")
          : test.kind === "failed" ? t("notif.testFailed")
            : test.r.ok ? t("notif.testOk", { code: test.r.http_status ?? "" })
              : t("notif.testRejected", { detail: [test.r.http_status ? `HTTP ${test.r.http_status}` : "", test.r.error].filter(Boolean).join(" · ") })}
      </p>}

      <div><h3 className="mb-2 font-semibold">{t("notif.recent")}</h3>
        {d.recent_deliveries.length === 0 ? <p className="muted">{t("notif.noneYet")}</p>
          : <div className="overflow-x-auto"><table className="w-full text-left text-sm">
            <thead><tr className="muted"><th className="p-2">{t("notif.col.time")}</th><th className="p-2">{t("notif.col.event")}</th><th className="p-2">{t("notif.col.result")}</th><th className="p-2">HTTP</th><th className="p-2">{t("notif.col.error")}</th></tr></thead>
            <tbody>{d.recent_deliveries.map((r, i) => <tr key={i} className="border-t border-line">
              <td className="mono whitespace-nowrap p-2">{fmtTime(r.at, lang)}</td><td className="mono p-2">{r.event}</td>
              <td className={`p-2 ${r.ok ? "text-green" : "text-red"}`}>● {r.ok ? t("notif.ok") : t("notif.failed")}</td>
              <td className="mono p-2">{r.http_status ?? "–"}</td><td className="mono p-2">{r.error || "–"}</td></tr>)}</tbody>
          </table></div>}
      </div>
    </div>}
  </Card>;
}
