import { useState, type FormEvent } from "react";
import { api, ApiError, contentUrl } from "./api";
import { usePoll } from "./hooks";
import { useI18n } from "./i18n";
import { Card, fmtBytes, fmtTime } from "./ui";

type Version = { id: number; version: number; sha256: string; size_bytes: number; created_at: string };
type Source = Version & { source_id: string; title: string; citation: string; versions: Version[] };
type Listing = { sources: Source[]; max_sources: number; max_bytes: number };

// SourcesCard holds the owner-pasted source texts of a Knowledge Shorts episode (D15). Everything the
// owner typed is rendered as React text (never HTML). The form is only seeded when "new version" is
// chosen, so a poll never overwrites what is being typed.
export function SourcesCard({ episodeId }: { episodeId: number }) {
  const { t, lang } = useI18n();
  const list = usePoll<Listing>(`/episodes/${episodeId}/sources`, 10000);
  const [editing, setEditing] = useState<string | null>(null); // source id being versioned, null = new source
  const [title, setTitle] = useState("");
  const [citation, setCitation] = useState("");
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [shown, setShown] = useState<Record<number, string | null>>({}); // artifact id -> text (null while loading)

  const view = async (v: Version) => {
    if (v.id in shown) { setShown(({ [v.id]: _drop, ...rest }) => rest); return; }
    setShown((s) => ({ ...s, [v.id]: null }));
    try {
      const r = await fetch(contentUrl(v.id));
      const body = r.ok ? await r.text() : t("app.error");
      setShown((s) => ({ ...s, [v.id]: body }));
    } catch { setShown((s) => ({ ...s, [v.id]: t("app.error") })); }
  };
  const startVersion = async (s: Source) => {
    // Load the text first so the form never shows a stale body that a late response would overwrite.
    let body = "";
    try { const r = await fetch(contentUrl(s.id)); if (r.ok) body = await r.text(); } catch { /* the owner can paste the text again */ }
    setEditing(s.source_id); setTitle(s.title); setCitation(s.citation); setText(body); setMsg(null);
  };
  const reset = () => { setEditing(null); setTitle(""); setCitation(""); setText(""); };
  const submit = async (e: FormEvent) => {
    e.preventDefault(); setBusy(true); setMsg(null);
    const path = editing ? `/episodes/${episodeId}/sources/${editing}/versions` : `/episodes/${episodeId}/sources`;
    try {
      const r = await api<{ source_id: string; version: number }>(path, { method: "POST", body: { title, citation, text } });
      setMsg({ ok: true, text: t("sources.saved", { id: r.source_id, n: r.version }) });
      reset(); list.reload();
    } catch (x) { setMsg({ ok: false, text: x instanceof ApiError ? x.message : t("app.error") }); }
    finally { setBusy(false); }
  };
  const d = list.data;
  const full = d !== null && d.sources.length >= d.max_sources;
  return <Card title={t("sources.title")}>
    <p className="muted mb-3 text-sm">{t("sources.help")}</p>
    {list.error && <p role="alert" className="text-red">{list.error.message}</p>}
    {d && (d.sources.length === 0 ? <p className="muted">{t("sources.empty")}</p> :
      <ul className="mb-4 flex flex-col gap-2">{d.sources.map((s) => <li key={s.source_id} className="inner">
        <div className="flex flex-wrap items-center gap-3">
          <strong className="mono">{s.source_id}</strong><span>{s.title}</span>
          <span className="mono text-xs">{t("sources.version", { n: s.version })} · {s.sha256.slice(0, 12)}</span>
          <span className="muted text-sm">{fmtBytes(s.size_bytes)} · {fmtTime(s.created_at, lang)}{s.versions.length > 1 ? ` · ${t("sources.versions", { n: s.versions.length })}` : ""}</span>
          <span className="ml-auto flex gap-2">
            <button type="button" className="btn text-xs" aria-pressed={s.id in shown} onClick={() => void view(s)}>{s.id in shown ? t("sources.hide") : t("sources.view")}</button>
            <button type="button" className="btn text-xs" onClick={() => void startVersion(s)}>{t("sources.newVersion")}</button>
          </span>
        </div>
        {s.citation && <p className="muted mt-1 whitespace-pre-wrap text-sm">{s.citation}</p>}
        {s.id in shown && <pre className="mono mt-2 max-h-64 overflow-auto whitespace-pre-wrap rounded-lg border border-line p-2 text-xs">{shown[s.id] ?? t("sources.loading")}</pre>}
      </li>)}</ul>)}
    {d && <p className="muted mb-2 text-xs">{t("sources.count", { n: d.sources.length, max: d.max_sources })}</p>}
    {(editing || !full) && <form onSubmit={(e) => void submit(e)} className="flex flex-col gap-3">
      {editing && <p className="text-sm">{t("sources.editing", { id: editing })}</p>}
      <div><label htmlFor="src-title" className="mb-1 block text-sm">{t("sources.fTitle")}</label>
        <input id="src-title" className="field w-full" value={title} maxLength={120} required onChange={(e) => setTitle(e.target.value)} /></div>
      <div><label htmlFor="src-cite" className="mb-1 block text-sm">{t("sources.fCitation")}</label>
        <textarea id="src-cite" className="field w-full" rows={2} value={citation} maxLength={500} onChange={(e) => setCitation(e.target.value)} /></div>
      <div><label htmlFor="src-text" className="mb-1 block text-sm">{t("sources.fText")}</label>
        <textarea id="src-text" className="field mono w-full" rows={8} value={text} required onChange={(e) => setText(e.target.value)} />
        <p className="muted mt-1 text-xs">{t("sources.textHint", { kb: Math.round((d?.max_bytes ?? 204800) / 1024) })}</p></div>
      <div className="flex flex-wrap items-center gap-2">
        <button className="btn btn-primary" disabled={busy}>{editing ? t("sources.addVersion", { id: editing }) : t("sources.add")}</button>
        {editing && <button type="button" className="btn" onClick={reset}>{t("sources.cancelEdit")}</button>}
      </div>
    </form>}
    {msg && <p role={msg.ok ? "status" : "alert"} className={`mt-2 text-sm ${msg.ok ? "text-green" : "text-red"}`}>{msg.text}</p>}
  </Card>;
}
