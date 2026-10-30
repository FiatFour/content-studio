import { useEffect, useState } from "react";
import { api, ApiError, contentUrl, type EpisodeSummary, type Review, type ShortsReview } from "../api";
import { usePoll } from "../hooks";
import { useI18n, type Key } from "../i18n";
import { Card, StateBadge, fmtBytes, fmtTime } from "../ui";

export function ReviewQueue() {
  const { t } = useI18n();
  const eps = usePoll<{ episodes: EpisodeSummary[] }>("/episodes", 3000);
  const ready = (eps.data?.episodes ?? []).filter((e) => e.latest_run?.state === "ready_for_review");
  return <Card title={t("review.queueTitle")}>
    {!eps.data ? <p className="muted">{t("app.loading")}</p> : ready.length === 0 ? <p className="muted">{t("review.queueEmpty")}</p> :
      <ul className="flex flex-col gap-3">{ready.map((e) => <li key={e.id} className="inner flex items-center gap-3"><span className="mr-auto font-semibold">{t("episodes.episode", { n: e.number })} · {e.title}</span>
        <a className="btn inline-flex items-center no-underline" href={`#/episodes/${e.id}`}>{t("episodes.open")}</a></li>)}</ul>}
  </Card>;
}

function useText(artifactId: number | null): string | null {
  const [text, setText] = useState<string | null>(null);
  useEffect(() => {
    setText(null);
    if (artifactId == null) return;
    let live = true;
    fetch(contentUrl(artifactId)).then((r) => r.text()).then((x) => live && setText(x)).catch(() => live && setText(""));
    return () => { live = false; };
  }, [artifactId]);
  return text;
}

export function ReviewPage({ id }: { id: number }) {
  const { t, lang } = useI18n();
  const r = usePoll<Review>(`/videos/${id}/review`, 2500);
  const [tab, setTab] = useState<"script" | "srt">("script");
  const [notes, setNotes] = useState("");
  const [msg, setMsg] = useState("");
  const script = useText(tab === "script" ? (r.data?.script_artifact_id ?? null) : null);
  const srt = useText(tab === "srt" ? (r.data?.subtitles_artifact_id ?? null) : null);
  if (!r.data) return <Card>{r.error ? <p role="alert" className="text-red">{t("review.none")}</p> : t("app.loading")}</Card>;
  const { video: v, qa, decisions, exports, versions } = r.data;
  const latest = decisions[0];
  const approved = latest?.decision === "approved" && latest.sha256 === v.sha256;
  const blockingFail = qa.some((q) => q.severity === "blocking" && !q.passed);
  const canApprove = v.validation_status === "qa_passed" && !blockingFail;
  const post = async (path: string, body?: unknown) => {
    try { await api(path, { method: "POST", body }); setMsg(""); r.reload(); return true; }
    catch (x) { setMsg(x instanceof ApiError ? x.message : t("app.error")); return false; }
  };
  const decide = async (decision: "approved" | "rejected" | "changes_requested") => {
    if (decision !== "approved" && notes.trim() === "") { setMsg(t("review.notesRequired")); return; }
    if (decision === "approved" && !confirm(t("review.confirmApprove", { n: v.version, hash: v.sha256.slice(0, 12) }))) return;
    if (await post(`/videos/${v.id}/decision`, { decision, notes, sha256: v.sha256 })) setNotes("");
  };
  const status = latest && latest.sha256 === v.sha256 ? latest.decision : "none";
  return <div className="grid gap-4 xl:grid-cols-[360px_1fr_340px]">
    <Card title={t("review.video")}>
      <video key={v.id} controls playsInline preload="metadata" src={contentUrl(v.id)} className="aspect-[9/16] w-full rounded-xl border-2 border-outline bg-black" />
      <dl className="mono mt-3 grid grid-cols-2 gap-1 text-sm">
        <dt className="muted">{t("review.size")}</dt><dd>{fmtBytes(v.size_bytes)}</dd>
        <dt className="muted">{t("review.cost")}</dt><dd>{t("review.costNone")}</dd></dl>
    </Card>
    <div className="flex flex-col gap-4">
      <Card title={t("review.qa")}>
        <p className="mono mb-2 text-sm">{t("review.qa.summary", { pass: qa.filter((q) => q.passed).length, fail: qa.filter((q) => !q.passed).length })}</p>
        <ul className="flex flex-col gap-2">{qa.map((q) => <li key={q.rule} className="inner flex flex-wrap items-baseline gap-x-3">
          <span className={`mono text-sm ${q.passed ? "text-green" : q.severity === "blocking" ? "text-red" : "text-amber"}`}><span aria-hidden>● </span>{q.passed ? t("review.qa.pass") : q.severity === "blocking" ? t("review.qa.fail") : t("review.qa.warning")}</span>
          <strong className="mono text-sm">{q.rule}</strong><span className="muted text-xs">{q.severity === "blocking" ? t("review.qa.blocking") : t("review.qa.warning")}</span>
          <span className="mono muted text-xs">{t("review.qa.version", { n: q.rule_version })} · {t("review.qa.artifact", { id: q.artifact_id })} · {t("review.qa.at", { when: fmtTime(q.created_at, lang) })}</span>
          <span className="w-full text-sm">{q.reason}</span>{!q.passed && q.suggested_fix && <span className="w-full text-sm text-amber">{t("review.qa.fix")}: {q.suggested_fix}</span>}</li>)}</ul>
        <p className="muted mt-3 text-xs">{t("review.qa.aiNote")}</p>
      </Card>
      {r.data.shorts && <ShortsCard s={r.data.shorts} />}
      <Card>
        <div role="tablist" className="mb-3 flex gap-2">{(["script", "srt"] as const).map((k) => <button key={k} role="tab" aria-selected={tab === k} className="btn" style={{ borderColor: tab === k ? "var(--color-blue)" : undefined }} onClick={() => setTab(k)}>{t(`review.tab.${k}` as Key)}</button>)}</div>
        <pre className="inner max-h-96 overflow-auto whitespace-pre-wrap text-sm">{(tab === "script" ? (script && prettyJSON(script)) : srt?.replaceAll("​", "")) ?? t("app.loading")}</pre>
      </Card>
    </div>
    <div className="flex flex-col gap-4">
      <Card title={t("review.decision")}>
        <p className="muted text-sm">{t("review.exactVersion")}</p>
        <div className="inner my-2"><div className="font-display font-extrabold">{t("episode.version", { n: v.version })}</div><div className="mono break-all text-xs">{v.sha256}</div>
          <div className="mt-2 flex flex-wrap gap-3"><StateBadge state={v.validation_status} /><span className={`mono text-sm ${status === "approved" ? "text-green" : "muted"}`}>{t(`review.status.${status}` as Key)}</span></div></div>
        {!canApprove && <p role="alert" className="mb-2 text-sm text-red">{t("review.notPassedQa")}</p>}
        <label htmlFor="notes" className="mb-1 block text-sm">{t("review.notes")}</label>
        <textarea id="notes" className="field" rows={3} value={notes} onChange={(e) => setNotes(e.target.value)} />
        <div className="mt-3 flex flex-wrap gap-2">
          <button className="btn btn-primary" disabled={!canApprove || approved} onClick={() => void decide("approved")}>{t("review.approve")}</button>
          <button className="btn" onClick={() => void decide("changes_requested")}>{t("review.requestChanges")}</button>
          <button className="btn btn-danger" onClick={() => void decide("rejected")}>{t("review.reject")}</button></div>
        {msg && <p role="alert" className="mt-2 text-sm text-red">{msg}</p>}
        <hr className="my-4 border-line" />
        <button className="btn w-full" disabled={!approved} onClick={() => void post(`/videos/${v.id}/export`)}>{t("review.export")}</button>
        <p className="muted mt-2 text-xs">{t("review.exportHelp")}</p>
        {exports.map((x) => <p key={x.id} className="mono mt-2 break-all text-xs text-green">{t("review.exported", { name: x.file_name })}</p>)}
      </Card>
      <Card title={t("review.history")}><ul className="flex flex-col gap-2">{versions.map((x) => <li key={x.id} className="inner"><a href={`#/review/${x.id}`} aria-current={x.id === v.id ? "page" : undefined} className="font-semibold">{t("episode.version", { n: x.version })}</a>
        <span className="mono ml-2 text-xs">{x.sha256.slice(0, 12)}</span><div className="muted text-xs">{fmtTime(x.created_at, lang)} · {t(`review.status.${x.decision ?? "none"}` as Key)}</div></li>)}</ul></Card>
      <Card title={t("review.decisions")}>{decisions.length === 0 ? <p className="muted">{t("review.noDecisions")}</p> : <ul className="flex flex-col gap-2">{decisions.map((d) => <li key={d.id} className="inner text-sm">
        <strong>{t(`review.status.${d.decision}` as Key)}</strong> <span className="muted">{t("review.by", { who: d.reviewer })} · {fmtTime(d.created_at, lang)}</span>
        <div className="mono text-xs">{d.sha256.slice(0, 12)}</div>{d.notes && <div>{d.notes}</div>}</li>)}</ul>}</Card>
    </div>
  </div>;
}

// ShortsCard lists, as plain text, what a Knowledge Shorts video rests on. It only shows data; it has no
// button and cannot approve anything. Source titles and quotes are owner or model text: React text only.
function ShortsCard({ s }: { s: ShortsReview }) {
  const { t } = useI18n();
  const cc = s.claim_check;
  return <Card title={t("review.shorts.title")}>
    {s.unreadable && <p role="alert" className="mb-2 text-sm text-red">{t("review.shorts.unreadable", { why: s.unreadable })}</p>}
    <h3 className="font-semibold">{t("review.shorts.sources")}</h3>
    <ul className="mb-3 flex flex-col gap-1">{s.sources.map((x) => <li key={x.artifact_id} className="mono break-all text-xs">{t("review.shorts.sourceLine", { id: x.source_id, title: x.title, n: x.version, sha: x.sha256.slice(0, 12) })}</li>)}</ul>
    <h3 className="font-semibold">{t("review.shorts.claims")}</h3>
    {s.claims.length === 0 ? <p className="muted text-sm">{t("review.shorts.noClaims")}</p> :
      <ul className="mb-3 flex flex-col gap-2">{s.claims.map((c) => <li key={c.id} className="inner text-sm">
        <strong className="mono">{c.id}</strong> · <span>{c.text}</span>
        <div className="muted mono text-xs">{t("review.shorts.claimSource", { id: c.source_id })}{s.claim_beats[c.id] ? ` · ${t("review.shorts.claimBeats", { beats: s.claim_beats[c.id].join(", ") })}` : ""}</div>
        <div className="text-xs">{t("review.shorts.quote")}: <q>{c.quote}</q></div></li>)}</ul>}
    <h3 className="font-semibold">{t("review.shorts.check")}</h3>
    <p className={`mono text-sm ${cc?.ok ? "text-green" : "text-red"}`}>{cc == null ? t("review.shorts.checkNone") : cc.ok ? t("review.shorts.checkOk", { claims: cc.claims, beats: cc.beats }) : t("review.shorts.checkFail", { n: cc.failures.length })}</p>
    {cc && !cc.ok && <ul className="text-sm">{cc.failures.map((f, i) => <li key={i}>{f.message}</li>)}</ul>}
    <p className="muted mt-3 text-xs">{t("review.shorts.note")}</p>
  </Card>;
}

function prettyJSON(s: string): string {
  try { return JSON.stringify(JSON.parse(s), null, 2); } catch { return s; }
}
