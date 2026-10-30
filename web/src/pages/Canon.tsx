import { useState } from "react";
import { api, ApiError, type CanonImpact, type CanonProposal } from "../api";
import { cannotCheckReason, groupFindings, prettyPayload, reasonLength, reasonValid, REASON_MAX, type FindingKind } from "../canon";
import { usePoll } from "../hooks";
import { dictionaries, useI18n, type Key } from "../i18n";
import { Card, fmtTime } from "../ui";

// Proposal text (payload, rationale, reasons, finding messages) is untrusted. It is only ever placed in the page
// as React text children, never as HTML. There is deliberately no create-proposal form here.

const statuses = ["", "proposed", "approved", "rejected"] as const;
const kindKey = (k: string): Key | null => (`canon.kind.${k}` in dictKeys ? (`canon.kind.${k}` as Key) : null);
const dictKeys = dictionaries.en;

const kindStyle: Record<FindingKind, string> = {
  conflict: "border-red text-red", warning: "border-amber text-amber", cannot: "border-line text-muted", info: "border-line text-blue",
};

export function CanonProposals() {
  const { t } = useI18n();
  const [status, setStatus] = useState<string>("");
  const list = usePoll<{ proposals: CanonProposal[] }>(`/canon/proposals?limit=200${status ? `&status=${status}` : ""}`, 5000);
  return <Card title={t("canon.title")}>
    <p className="muted mb-3 text-sm">{t("canon.intro")}</p>
    <div role="group" aria-label={t("canon.filter")} className="mb-3 flex flex-wrap items-center gap-2">
      <span className="muted text-sm">{t("canon.filter")}</span>
      {statuses.map((s) => <button key={s || "all"} className="btn" aria-pressed={status === s} style={{ borderColor: status === s ? "var(--color-blue)" : undefined }}
        onClick={() => setStatus(s)}>{s ? t(`canon.status.${s}` as Key) : t("canon.filter.all")}</button>)}
    </div>
    {!list.data ? <p className={list.error ? "text-red" : "muted"} role={list.error ? "alert" : undefined}>{list.error ? t("canon.loadFailed") : t("app.loading")}</p>
      : list.data.proposals.length === 0 ? <p className="muted">{t("canon.empty")}</p>
        : <ul className="flex flex-col gap-3">{list.data.proposals.map((p) => <ProposalRow key={p.id} p={p} />)}</ul>}
    <p className="muted mt-4 text-xs">{t("canon.noCreate")}</p>
  </Card>;
}

function ProposalRow({ p }: { p: CanonProposal }) {
  const { t, lang } = useI18n();
  const k = kindKey(p.kind);
  return <li className="inner flex flex-wrap items-center gap-3">
    <div className="mr-auto min-w-0">
      <div><span className="mono muted text-sm">#{p.id}</span> <span className="font-semibold">{k ? t(k) : p.kind}</span></div>
      <div className="muted text-sm">{t("canon.proposedBy", { who: p.proposed_by })} · {fmtTime(p.created_at, lang)}</div>
    </div>
    <CanonStatus s={p.status} />
    <a className="btn inline-flex items-center no-underline" href={`#/canon/${p.id}`}>{t("canon.open")}</a>
  </li>;
}

export function CanonProposalPage({ id }: { id: number }) {
  const { t, lang } = useI18n();
  const prop = usePoll<CanonProposal>(`/canon/proposals/${id}`, 4000);
  const impact = usePoll<CanonImpact>(`/canon/proposals/${id}/impact`, 4000);
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState("");
  const back = <a className="ml-3 text-sm" href="#/canon">{t("canon.back")}</a>;
  if (!prop.data) return <Card title={<>{t("canon.title")} {back}</>}>{prop.error ? <p role="alert" className="text-red">{prop.error.status === 404 ? t("canon.notFound") : t("canon.loadFailed")}</p> : <p className="muted">{t("app.loading")}</p>}</Card>;
  const p = prop.data;
  const k = kindKey(p.kind);
  const groups = impact.data ? groupFindings(impact.data.findings) : [];
  const conflicts = impact.data ? impact.data.findings.filter((f) => f.severity === "conflict").length : 0;
  const pending = p.status === "proposed";
  const decide = async (approve: boolean) => {
    if (!reasonValid(reason)) { setMsg(t("canon.reason")); return; }
    if (approve && !confirm(t("canon.confirmApprove", { id: p.id }))) return;
    setBusy(true); setMsg("");
    try {
      await api(`/canon/proposals/${p.id}/${approve ? "approve" : "reject"}`, { method: "POST", body: { reason: reason.trim() } });
      setReason("");
    } catch (x) { setMsg(x instanceof ApiError ? x.message : t("app.error")); }
    // Always re-read: what the page shows after a decision is what the server stored, success or not.
    prop.reload(); impact.reload(); setBusy(false);
  };
  return <div className="grid gap-4 xl:grid-cols-[1fr_380px]">
    <div className="flex flex-col gap-4">
      <Card title={<><span className="mono muted">#{p.id}</span> {k ? t(k) : p.kind} {back}</>}>
        <div className="mb-2 flex flex-wrap items-center gap-3"><CanonStatus s={p.status} /><span className="muted text-sm">{t("canon.proposedBy", { who: p.proposed_by })} · {fmtTime(p.created_at, lang)}</span></div>
        {p.episode_id != null && <p className="muted text-sm">{t("canon.episode", { id: p.episode_id })}</p>}
        <h3 className="mt-3 text-sm font-semibold">{t("canon.rationale")}</h3>
        <p className="inner whitespace-pre-wrap break-words text-sm">{p.rationale}</p>
        <h3 className="mt-3 text-sm font-semibold">{t("canon.payload")}</h3>
        <pre data-testid="payload" className="inner max-h-96 overflow-auto whitespace-pre-wrap break-words text-sm">{prettyPayload(p.payload)}</pre>
        {!pending && <div className="mt-3 text-sm" data-testid="decision">
          <strong>{t(`canon.status.${p.status}` as Key)}</strong> <span className="muted">{t("canon.decidedBy", { status: t(`canon.status.${p.status}` as Key), who: p.decided_by ?? "", when: p.decided_at ? fmtTime(p.decided_at, lang) : "" })}</span>
          {p.reason && <p className="whitespace-pre-wrap break-words">{t("canon.decidedReason", { reason: p.reason })}</p>}</div>}
      </Card>
      <Card title={t("canon.impact")}>
        {!impact.data ? <p className={impact.error ? "text-red" : "muted"} role={impact.error ? "alert" : undefined}>{impact.error ? t("canon.impactFailed") : t("app.loading")}</p> : <ImpactView r={impact.data} />}
      </Card>
    </div>
    <div className="flex flex-col gap-4">
      {conflicts > 0 && <div role="alert" className="card border-red text-red" data-testid="conflict-banner"><strong>{t("canon.conflictBanner", { n: conflicts })}</strong></div>}
      <Card title={t("canon.findings")}>
        <p className="muted mb-2 text-xs">{t("canon.findings.advisory")}</p>
        {!impact.data ? <p className="muted">{t("app.loading")}</p> : groups.length === 0 ? <p className="muted">{t("canon.findings.none")}</p> :
          <ul className="flex flex-col gap-2">{groups.flatMap((g) => g.items.map((f, i) => <li key={`${g.kind}${i}${f.code}`} data-kind={g.kind} className={`inner border-2 text-sm ${kindStyle[g.kind]}`}>
            <span className="mono text-xs"><span aria-hidden>● </span>{t(`canon.findings.${g.kind}` as Key)} · {f.code}</span>
            <div className="text-ink">{g.kind === "cannot" ? cannotCheckReason(f.message) : f.message}</div></li>))}</ul>}
        <p className="muted mt-2 text-xs">{t("canon.findings.english")}</p>
      </Card>
      {pending && <Card title={t("canon.decide")}>
        <label htmlFor="reason" className="mb-1 block text-sm">{t("canon.reason")}</label>
        <textarea id="reason" className="field" rows={4} value={reason} maxLength={REASON_MAX * 2} onChange={(e) => setReason(e.target.value)} />
        <div className={`mono mt-1 text-xs ${reasonLength(reason) > REASON_MAX ? "text-red" : "muted"}`}>{t("canon.reasonCount", { n: reasonLength(reason) })}</div>
        <div className="mt-3 flex flex-wrap gap-2">
          <button className="btn btn-primary" disabled={busy || !reasonValid(reason)} onClick={() => void decide(true)}>{busy ? t("canon.deciding") : t("canon.approve")}</button>
          <button className="btn btn-danger" disabled={busy || !reasonValid(reason)} onClick={() => void decide(false)}>{t("canon.reject")}</button></div>
        {msg && <p role="alert" className="mt-2 text-sm text-red">{msg}</p>}
      </Card>}
    </div>
  </div>;
}

const statusColor: Record<string, string> = { proposed: "text-amber", approved: "text-green", rejected: "text-red" };
function CanonStatus({ s }: { s: string }) {
  const { t } = useI18n();
  return <span className={`inline-flex items-center gap-2 mono text-sm ${statusColor[s] ?? "text-muted"}`}><span aria-hidden className="h-2.5 w-2.5 rounded-full bg-current" />{t(`canon.status.${s}` as Key)}</span>;
}

function ImpactView({ r }: { r: CanonImpact }) {
  const { t } = useI18n();
  const { characters, relationships, episodes } = r.impact;
  if (characters.length + relationships.length + episodes.length === 0) return <p className="muted">{t("canon.impact.none")}</p>;
  return <div className="flex flex-col gap-3 text-sm">
    {characters.length > 0 && <div><h3 className="font-semibold">{t("canon.impact.characters")}</h3><ul>{characters.map((c) => <li key={c.slug}>{c.name || c.slug} <span className="mono muted text-xs">{c.slug}</span>{!c.exists && <span className="ml-2 text-amber">{t("canon.impact.notCanon")}</span>}</li>)}</ul></div>}
    {relationships.length > 0 && <div><h3 className="font-semibold">{t("canon.impact.relationships")}</h3><ul>{relationships.map((x) => <li key={x.id} className="mono text-xs">{x.from} → {x.to} · {x.type}</li>)}</ul></div>}
    {episodes.length > 0 && <div><h3 className="font-semibold">{t("canon.impact.episodes")}</h3><ul>{episodes.map((e) => <li key={e.id}>#{e.number} {e.title} <span className="muted text-xs">{e.in_story_date ?? ""} · {e.reason === "present" || e.reason === "source" ? t(`canon.impact.reason.${e.reason}` as Key) : e.reason}</span></li>)}</ul></div>}
  </div>;
}
