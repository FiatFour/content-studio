import { useState } from "react";
import { api, ApiError, type Monitor, type Notifications, type NotifTestResult, type Policies, type ProviderHealth, type Resource } from "../api";
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
    <HealthCard />
    <MonitorCard />
    <ResourcesCard />
    <PoliciesCard />
    <NotificationsCard />
  </div>;
}

// HealthCard shows read-only probes of the local AI services. The server decides what is probed;
// only the scheme and host:port of each configured address are shown.
function HealthCard() {
  const { t, lang } = useI18n();
  const h = usePoll<{ providers: ProviderHealth[] }>("/providers/health", 15000);
  const reason = (r?: string) => { if (!r) return ""; const k = `health.reason.${r}`; return k in dictionaries[lang] ? t(k as Key) : r; };
  return <Card title={t("health.title")}>
    <p className="muted mb-3">{t("health.help")}</p>
    {!h.data ? <p className={h.error ? "text-red" : "muted"} role={h.error ? "alert" : undefined}>{h.error ? t("health.loadFailed") : t("app.loading")}</p> : <div className="flex flex-col gap-3">
      <ul className="flex flex-col gap-2">
        {h.data.providers.map((p) => <li key={p.name} className="inner flex flex-wrap items-center gap-3">
          <span className="mono mr-auto font-semibold">{p.name} <span className="muted text-sm">({p.capability})</span></span>
          {p.endpoint && <span className="mono muted text-sm">{p.endpoint}</span>}
          <span className={`mono inline-flex items-center gap-2 ${p.state === "up" ? "text-green" : p.state === "down" ? "text-red" : "text-amber"}`}>
            <span aria-hidden className="h-2.5 w-2.5 rounded-full bg-current" />{t(`health.${p.state}`)}</span>
          {p.latency_ms !== undefined && <span className="mono text-sm">{t("health.latency", { ms: p.latency_ms })}</span>}
          {p.reason && <span className="muted text-sm">{reason(p.reason)}</span>}
          <span className="muted mono text-sm">{fmtTime(p.checked_at, lang)}</span>
        </li>)}
      </ul>
      <div><button className="btn" onClick={h.reload}>{t("health.refresh")}</button></div>
    </div>}
  </Card>;
}

// MonitorCard is read-only: GPU (nvidia-smi), GPU slot, queue and disk space. A value the server could
// not measure is shown as unavailable, never as zero.
function MonitorCard() {
  const { t } = useI18n();
  const m = usePoll<Monitor>("/monitor", 5000);
  const d = m.data;
  const n = (v: number | undefined, unit: string) => (v === undefined ? "–" : `${v} ${unit}`);
  return <Card title={t("mon.title")}>
    <p className="muted mb-3">{t("mon.help")}</p>
    {!d ? <p className={m.error ? "text-red" : "muted"} role={m.error ? "alert" : undefined}>{m.error ? t("mon.loadFailed") : t("app.loading")}</p> : <div className="flex flex-col gap-3">
      <div className="inner flex flex-col gap-1">
        <h3 className="font-semibold">{t("mon.gpu")}</h3>
        {!d.gpu.available ? <p className="text-amber">{t("mon.gpuUnavailable")}</p> : <div className="mono flex flex-wrap gap-4 text-sm">
          <span>{d.gpu.name}</span>
          <span>{t("mon.util")}: {n(d.gpu.util_pct, "%")}</span>
          <span>{t("mon.temp")}: {n(d.gpu.temp_c, "°C")}</span>
          <span>{t("mon.vram")}: {d.gpu.vram_used_mib ?? "–"} / {n(d.gpu.vram_total_mib, "MiB")}</span>
        </div>}
      </div>
      <div className="inner flex flex-col gap-1">
        <h3 className="font-semibold">{t("mon.slot")}</h3>
        <p className="mono text-sm">{d.gpu_slot.running === 0 ? t("mon.slotFree") : t("mon.slotBusy", { stage: d.gpu_slot.stage ?? "?", episode: d.gpu_slot.episode ? ` · ${d.gpu_slot.episode}` : "" })}</p>
        <p className="muted text-sm">{t("mon.slotCount", { running: d.gpu_slot.running, max: d.gpu_slot.max_concurrent, waiting: d.gpu_slot.waiting })}</p>
      </div>
      <div className="inner flex flex-col gap-1">
        <h3 className="font-semibold">{t("mon.queue")}</h3>
        <div className="mono flex flex-wrap gap-4 text-sm">
          <span>{t("mon.queued")}: {d.queue.queued}</span><span>{t("mon.running")}: {d.queue.running}</span>
          <span>{t("mon.waiting")}: {d.queue.waiting_for_resource}</span><span>{t("mon.retry")}: {d.queue.retry_scheduled}</span>
        </div>
      </div>
      <div className="inner flex flex-col gap-1">
        <h3 className="font-semibold">{t("mon.disk")}</h3>
        {d.disks.map((k) => <p key={k.name} className="mono text-sm">{t(k.name === "exports" ? "mon.disk.exports" : "mon.disk.data")}: {k.free_gib === null || k.total_gib === null ? t("mon.diskUnavailable") : t("mon.diskFree", { free: k.free_gib.toFixed(1), total: k.total_gib.toFixed(1) })}</p>)}
      </div>
    </div>}
  </Card>;
}

// ResourcesCard shows each limited resource with its live usage and changes its limit. A value above
// the safe maximum (GPU: 1) needs a second, explicit confirmation, which the API also requires.
function ResourcesCard() {
  const { t } = useI18n();
  const r = usePoll<{ resources: Resource[] }>("/resources", 5000);
  const [draft, setDraft] = useState<Record<string, string>>({});
  const [confirming, setConfirming] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; key: Key; vars?: Record<string, string | number> } | null>(null);

  const apply = async (res: Resource, n: number) => {
    setBusy(true); setMsg(null);
    try {
      await api(`/resources/${encodeURIComponent(res.name)}`, { method: "PUT", body: { max_concurrent: n, confirm_unsafe: n > res.safe_max } });
      setConfirming(null); setDraft((d) => { const { [res.name]: _gone, ...rest } = d; return rest; });
      setMsg({ ok: true, key: "res.saved", vars: { name: res.name, n } }); r.reload();
    } catch { setMsg({ ok: false, key: "res.saveFailed" }); } finally { setBusy(false); }
  };

  return <Card title={t("res.title")}>
    <p className="muted mb-3">{t("res.help")}</p>
    {!r.data ? <p className={r.error ? "text-red" : "muted"} role={r.error ? "alert" : undefined}>{r.error ? t("res.loadFailed") : t("app.loading")}</p> : <ul className="flex flex-col gap-3">
      {r.data.resources.map((res) => {
        const value = draft[res.name] ?? String(res.max_concurrent);
        const n = Number(value);
        const valid = Number.isInteger(n) && n >= 1 && n <= res.max;
        const unsafe = valid && n > res.safe_max;
        return <li key={res.name} className="inner flex flex-col gap-3">
          <div className="flex flex-wrap items-center gap-3">
            <span className="mono mr-auto font-semibold">{res.name}</span>
            <span className="mono text-sm">{t("res.current")}: {res.max_concurrent}</span>
            <span className="mono text-sm">{t("res.running")}: {res.running}</span>
            <span className="mono text-sm">{t("res.waiting")}: {res.waiting}</span>
          </div>
          <form className="flex flex-wrap items-end gap-3" onSubmit={(e) => { e.preventDefault(); if (!valid || n === res.max_concurrent) return; if (unsafe) setConfirming(res.name); else void apply(res, n); }}>
            <div><label htmlFor={`lim-${res.name}`} className="mb-1 block text-sm">{t("res.limit", { max: res.max })}</label>
              <input id={`lim-${res.name}`} className="field !w-28" type="number" min={1} max={res.max} step={1} value={value} disabled={busy}
                onChange={(e) => { setConfirming(null); setMsg(null); setDraft({ ...draft, [res.name]: e.target.value }); }} /></div>
            <button className="btn btn-primary" disabled={busy || !valid || n === res.max_concurrent}>{t("res.save")}</button>
            {!valid && <span role="alert" className="text-sm text-red">{t("res.invalid", { max: res.max })}</span>}
          </form>
          {confirming === res.name && unsafe && <div role="alertdialog" aria-labelledby={`warn-${res.name}`} className="rounded-xl border-2 border-amber p-3">
            <p id={`warn-${res.name}`} className="font-semibold text-amber">{t("res.warnTitle")}</p>
            <p className="mb-3">{t("res.warn", { n })}</p>
            <div className="flex flex-wrap gap-2">
              <button className="btn btn-danger" disabled={busy} onClick={() => void apply(res, n)}>{busy ? t("res.saving") : t("res.confirm", { n })}</button>
              <button className="btn" disabled={busy} onClick={() => setConfirming(null)}>{t("app.cancel")}</button>
            </div>
          </div>}
          <p className="muted text-sm">{t("res.lowerHelp")}</p>
        </li>;
      })}
    </ul>}
    {msg && <p role={msg.ok ? "status" : "alert"} className={`mt-3 ${msg.ok ? "text-green" : "text-red"}`}>{t(msg.key, msg.vars)}</p>}
  </Card>;
}

// PoliciesCard is read-only: every task runs LOCAL_ONLY and changing that is not built (needs the budget, #19).
function PoliciesCard() {
  const { t } = useI18n();
  const p = usePoll<Policies>("/policies", 30000);
  return <Card title={t("pol.title")}>
    {!p.data ? <p className={p.error ? "text-red" : "muted"} role={p.error ? "alert" : undefined}>{p.error ? t("pol.loadFailed") : t("app.loading")}</p> : <div className="flex flex-col gap-3">
      <p className={`mono inline-flex items-center gap-2 ${p.data.cloud_enabled ? "text-amber" : "text-green"}`}>
        <span aria-hidden className="h-2.5 w-2.5 rounded-full bg-current" />{p.data.cloud_enabled ? t("pol.cloudOn") : t("pol.cloudOff")}</p>
      <div className="overflow-x-auto"><table className="w-full text-left text-sm">
        <thead><tr className="muted"><th className="p-2">{t("pol.task")}</th><th className="p-2">{t("pol.capability")}</th><th className="p-2">{t("pol.policy")}</th></tr></thead>
        <tbody>{p.data.tasks.map((x) => <tr key={x.task} className="border-t border-line">
          <td className="mono p-2">{x.task}</td><td className="mono p-2">{x.capability}</td><td className="mono p-2">{x.policy}</td></tr>)}</tbody>
      </table></div>
      <p className="muted"><span className="mono rounded border border-line px-1.5 text-sm">{t("app.notImplemented")}</span> {t("pol.changeHelp")}</p>
    </div>}
  </Card>;
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
