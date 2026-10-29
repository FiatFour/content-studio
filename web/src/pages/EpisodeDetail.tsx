import { useState, type FormEvent } from "react";
import { api, ApiError, contentUrl, type ArtifactRow, type Character, type EpisodeSummaryText, type Run, type VideoVersion } from "../api";
import { usePoll } from "../hooks";
import { useI18n, type Key } from "../i18n";
import { Bar, Card, StateBadge, agentColor, fmtBytes, fmtTime } from "../ui";

type Detail = {
  episode: { id: number; number: number; title: string; idea: string; target_duration_s: number; in_story_date: string | null };
  runs: Run[]; videos: VideoVersion[]; summary: EpisodeSummaryText | null;
};

// CanonCard edits the owner-written story bible entries of one episode. Its form state is seeded once
// from the loaded episode, so the 1.5 s poll of the page never overwrites what is being typed.
function CanonCard({ id, inStoryDate, summary }: { id: number; inStoryDate: string | null; summary: EpisodeSummaryText | null }) {
  const { t } = useI18n();
  const chars = usePoll<{ characters: Character[] }>("/characters", 10000);
  const [date, setDate] = useState(inStoryDate ?? "");
  const [text, setText] = useState(summary?.summary ?? "");
  const [present, setPresent] = useState<string[]>(summary?.characters_present ?? []);
  const [threads, setThreads] = useState((summary?.open_threads ?? []).join("\n"));
  const [dateMsg, setDateMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [sumMsg, setSumMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const run = async (path: string, body: unknown, done: (m: { ok: boolean; text: string }) => void) => {
    setBusy(true); done({ ok: true, text: t("episode.saving") });
    try { await api(path, { method: "PUT", body }); done({ ok: true, text: t("episode.saved") }); }
    catch (x) { done({ ok: false, text: x instanceof ApiError ? x.message : t("app.error") }); }
    finally { setBusy(false); }
  };
  const saveDate = (e: FormEvent) => { e.preventDefault(); void run(`/episodes/${id}/in-story-date`, { in_story_date: date }, setDateMsg); };
  const saveSummary = (e: FormEvent) => {
    e.preventDefault();
    void run(`/episodes/${id}/summary`, { summary: text, characters_present: present, open_threads: threads.split("\n") }, setSumMsg);
  };
  const msg = (m: { ok: boolean; text: string } | null) => m && <p role={m.ok ? "status" : "alert"} className={`mt-2 text-sm ${m.ok ? "text-green" : "text-red"}`}>{m.text}</p>;
  return <Card title={t("episode.canon.title")}>
    <form onSubmit={saveDate} className="mb-4 flex flex-wrap items-end gap-3">
      <div><label htmlFor="ep-date" className="mb-1 block text-sm">{t("episode.inStoryDate")}</label>
        <input id="ep-date" type="date" className="field mono" value={date} onChange={(e) => { setDate(e.target.value); setDateMsg(null); }} /></div>
      <button className="btn" disabled={busy}>{t("episode.saveDate")}</button>
      <p className="muted w-full text-xs">{t("episode.inStoryDateHint")}</p>{msg(dateMsg)}
    </form>
    <form onSubmit={saveSummary} className="flex flex-col gap-3">
      <div><label htmlFor="ep-sum" className="mb-1 block text-sm">{t("episode.summary")}</label>
        <textarea id="ep-sum" className="field" rows={4} value={text} maxLength={2000} required onChange={(e) => { setText(e.target.value); setSumMsg(null); }} />
        <p className="muted mt-1 text-xs">{t("episode.summaryHint")}{summary ? "" : ` ${t("episode.noSummary")}`}</p></div>
      <fieldset><legend className="mb-1 text-sm">{t("episode.present")}</legend>
        {!chars.data || chars.data.characters.length === 0 ? <p className="muted text-sm">{t("episode.noCharacters")} <a href="#/characters">{t("menu.characters")}</a></p> :
          <div className="flex flex-wrap gap-3">{chars.data.characters.map((c) => <label key={c.slug} className="inner flex min-h-11 items-center gap-2">
            <input type="checkbox" checked={present.includes(c.slug)} onChange={(e) => { setPresent(e.target.checked ? [...present, c.slug] : present.filter((s) => s !== c.slug)); setSumMsg(null); }} />{c.name}</label>)}</div>}</fieldset>
      <div><label htmlFor="ep-threads" className="mb-1 block text-sm">{t("episode.threads")}</label>
        <textarea id="ep-threads" className="field" rows={3} value={threads} onChange={(e) => { setThreads(e.target.value); setSumMsg(null); }} /></div>
      <div><button className="btn btn-primary" disabled={busy}>{t("episode.saveSummary")}</button>{msg(sumMsg)}</div>
    </form>
  </Card>;
}

export function EpisodeDetail({ id }: { id: number }) {
  const { t, lang } = useI18n();
  const d = usePoll<Detail>(`/episodes/${id}`, 1500);
  const run = d.data?.runs[0];
  const arts = usePoll<{ artifacts: ArtifactRow[] }>(run ? `/runs/${run.id}/artifacts` : null, 2500);
  const [msg, setMsg] = useState("");
  const act = async (path: string) => {
    try { await api(path, { method: "POST" }); setMsg(""); d.reload(); }
    catch (x) { setMsg(x instanceof ApiError ? x.message : t("app.error")); }
  };
  if (!d.data) return <Card>{d.error ? <p role="alert" className="text-red">{d.error.message}</p> : t("app.loading")}</Card>;
  const { episode: e, videos, summary } = d.data;
  const images = (arts.data?.artifacts ?? []).filter((a) => a.type === "image");
  const failed = run?.jobs.find((j) => j.state === "failed");
  return <div className="flex flex-col gap-4">
    <a href="#/episodes">← {t("episode.back")}</a>
    <Card><h1 className="font-display text-2xl font-extrabold">{t("episodes.episode", { n: e.number })} · {e.title}</h1>
      <p className="muted mt-1"><strong>{t("episode.idea")}:</strong> {e.idea}</p><p className="muted text-sm">{t("episodes.target", { s: e.target_duration_s })}</p></Card>
    <CanonCard id={e.id} inStoryDate={e.in_story_date} summary={summary} />
    {run && <Card title={<span className="flex flex-wrap items-center gap-3">{t("episode.stages")} <StateBadge state={run.state} /> <span className="muted text-sm">{t(`run.${run.kind}` as Key)} #{run.id}</span></span>}>
      <ol className="grid gap-2 sm:grid-cols-3 lg:grid-cols-6">{run.stages.map((s) => { const j = run.jobs.find((x) => x.stage === s);
        return <li key={s} className="inner" style={{ borderTop: `4px solid ${agentColor[s]}` }}>
          <div className="font-semibold">{t(`stage.${s}` as Key)}</div><div className="muted text-xs">{t(`agent.${s}` as Key)}</div>
          <div className="mt-2">{j ? <StateBadge state={j.state} /> : <span className="muted mono text-sm">{t("state.pending")}</span>}</div>
          {j && j.state === "running" && (j.progress_total > 0 ? <div className="mt-2"><Bar done={j.progress_done} total={j.progress_total} /><div className="mono mt-1 text-xs">{t("episode.progress", { done: j.progress_done, total: j.progress_total })}</div></div> : <p className="muted mt-2 text-xs">{t("episode.noProgress")}</p>)}
          {j && j.state === "waiting_for_resource" && j.wait_reason && <p className="muted mt-2 text-xs"><strong>{t("episode.waitingWhy")}:</strong> {j.wait_reason}</p>}
          {j && <div className="muted mono mt-2 text-xs">{t("episode.attempt", { n: j.attempt })}{j.model ? ` · ${j.model}` : ""}</div>}</li>; })}</ol>
      {failed && <p role="alert" className="mt-3 text-red"><strong>{t("episode.failedWhy")}:</strong> <span className="mono">{failed.error_class}</span> {failed.error_message}</p>}
      <div className="mt-3 flex flex-wrap gap-2">
        {run.state === "running" && <button className="btn btn-danger" onClick={() => void act(`/runs/${run.id}/cancel`)}>{t("episode.cancelRun")}</button>}
        {run.state === "failed" && <button className="btn btn-primary" onClick={() => void act(`/runs/${run.id}/retry`)}>{t("episode.retryRun")}</button>}
        {run.state !== "running" && <button className="btn" onClick={() => void act(`/episodes/${e.id}/rerender`)}>{t("episode.rerender")}</button>}
      </div>
      {msg && <p role="alert" className="mt-2 text-red">{msg}</p>}
      <p className="muted mt-2 text-xs">{t("episode.rerenderHelp")}</p>
    </Card>}
    {d.data.runs.length > 1 && <Card title={t("episode.runs")}><ul className="flex flex-col gap-2">{d.data.runs.map((r) => { const why = r.jobs.find((j) => j.state === "failed");
      return <li key={r.id} className="inner"><div className="flex flex-wrap items-center gap-3"><strong>#{r.id}</strong><span>{t(`run.${r.kind}` as Key)}</span><StateBadge state={r.state} />
        {r.kind === "rewrite" && r.parent_run_id !== null && <span className="muted text-sm">{t("episode.rewriteOf", { id: r.parent_run_id })}</span>}</div>
        {r.feedback && <p className="muted mt-2 text-sm"><strong>{t("episode.rewriteWhy")}:</strong> {r.feedback}</p>}
        {why && <p className="mt-2 text-sm text-red"><strong>{t("episode.failedWhy")}:</strong> <span className="mono">{why.error_class}</span> {why.error_message}</p>}</li>; })}</ul></Card>}
    <Card title={t("episode.shots")}>{images.length === 0 ? <p className="muted">{t("episode.noShots")}</p> :
      <ul className="grid grid-cols-3 gap-3 sm:grid-cols-4 lg:grid-cols-6">{images.map((a) => <li key={a.id}><img src={contentUrl(a.id)} alt={a.item_key} loading="lazy" className="aspect-[9/16] w-full rounded-lg border-2 border-outline object-cover" /><div className="mono text-center text-xs">{a.item_key}</div></li>)}</ul>}</Card>
    <Card title={t("episode.videos")}>{videos.length === 0 ? <p className="muted">{t("episode.noVideos")}</p> :
      <ul className="flex flex-col gap-2">{videos.map((v) => <li key={v.id} className="inner flex flex-wrap items-center gap-3">
        <strong>{t("episode.version", { n: v.version })}</strong><StateBadge state={v.validation_status} /><span className="mono text-xs">{v.sha256.slice(0, 12)}</span>
        <span className="muted text-sm">{fmtBytes(v.size_bytes)} · {fmtTime(v.created_at, lang)}</span>
        <span className="mono text-sm">{t(`review.status.${v.decision ?? "none"}` as Key)}</span><a className="btn ml-auto inline-flex items-center no-underline" href={`#/review/${v.id}`}>{t("episode.openReview")}</a></li>)}</ul>}</Card>
    <Card title={t("episode.artifacts")}>{(arts.data?.artifacts.length ?? 0) === 0 ? <p className="muted">{t("app.na")}</p> :
      <div className="max-h-64 overflow-auto"><table className="w-full text-left mono text-xs"><tbody>{arts.data!.artifacts.map((a) => <tr key={a.id} className="border-t border-line"><td className="py-1">{a.type}{a.item_key ? `/${a.item_key}` : ""}</td><td>v{a.version}</td><td>{a.sha256.slice(0, 12)}</td><td className="muted">{a.model}</td></tr>)}</tbody></table></div>}</Card>
  </div>;
}
