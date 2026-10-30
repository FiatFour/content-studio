import { useState, type FormEvent } from "react";
import { api, ApiError, type EpisodeSummary } from "../api";
import { profiles, profileLabel, type ProfileId } from "../profiles";
import { usePoll } from "../hooks";
import { useI18n, type Key } from "../i18n";
import { Bar, Card, StateBadge, fmtTime } from "../ui";

export function Episodes() {
  const { t, lang } = useI18n();
  const eps = usePoll<{ episodes: EpisodeSummary[] }>("/episodes", 2500);
  const [title, setTitle] = useState("");
  const [idea, setIdea] = useState("");
  const [len, setLen] = useState(60);
  const [profile, setProfile] = useState<ProfileId>("fictional_series");
  const rng = profiles.find((x) => x.id === profile) ?? profiles[0];
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setErr("");
    try {
      const r = await api<{ episode_id: number }>("/episodes", { method: "POST", body: { title, idea, target_duration_s: len, profile } });
      location.hash = `#/episodes/${r.episode_id}`;
    } catch (x) { setErr(x instanceof ApiError ? x.message : t("app.error")); }
    finally { setBusy(false); }
  };
  return <div className="flex flex-col gap-4">
    <Card title={t("episodes.new")}>
      <form onSubmit={(e) => void submit(e)} className="grid gap-3 md:grid-cols-2">
        <div><label htmlFor="ep-title" className="mb-1 block text-sm">{t("episodes.form.title")}</label>
          <input id="ep-title" className="field" value={title} maxLength={120} required onChange={(e) => setTitle(e.target.value)} /></div>
        <div><label htmlFor="ep-profile" className="mb-1 block text-sm">{t("episodes.form.profile")}</label>
          <select id="ep-profile" className="field" value={profile} onChange={(e) => {
            const next = e.target.value as ProfileId; setProfile(next);
            const r = profiles.find((x) => x.id === next); if (r) setLen((v) => Math.min(Math.max(v, r.min), r.max));
          }}>
            {profiles.map((x) => <option key={x.id} value={x.id}>{t(profileLabel(x.id))}{x.runnable ? "" : ` (${t("app.notImplemented")})`}</option>)}
          </select>
          {!rng.runnable && <p role="status" className="muted mt-1 text-xs">{t("episodes.form.profileNotImplemented")}</p>}</div>
        <div><label htmlFor="ep-len" className="mb-1 block text-sm">{t("episodes.form.length")}</label>
          <input id="ep-len" className="field mono" type="number" min={rng.min} max={rng.max} step={5} value={len} onChange={(e) => setLen(Number(e.target.value))} />
          <p className="muted mt-1 text-xs">{t("episodes.form.lengthHint", { min: rng.min, max: rng.max })}</p></div>
        <div className="md:col-span-2"><label htmlFor="ep-idea" className="mb-1 block text-sm">{t("episodes.form.idea")}</label>
          <textarea id="ep-idea" className="field" rows={4} value={idea} minLength={10} maxLength={2000} required onChange={(e) => setIdea(e.target.value)} />
          <p className="muted mt-1 text-xs">{t("episodes.form.ideaHint")}</p></div>
        {err && <p role="alert" className="text-red md:col-span-2">{err}</p>}
        <div className="md:col-span-2"><button className="btn btn-primary" disabled={busy || !rng.runnable}>{busy ? t("episodes.form.submitting") : t("episodes.form.submit")}</button></div>
      </form>
    </Card>
    <Card title={t("episodes.title")}>
      {!eps.data ? <p className="muted">{t("app.loading")}</p> : eps.data.episodes.length === 0 ? <p className="muted">{t("episodes.none")}</p> :
        <ul className="flex flex-col gap-3">{eps.data.episodes.map((e) => { const r = e.latest_run; const j = r?.jobs.find((x) => x.state === "running") ?? r?.jobs.at(-1);
          return <li key={e.id} className="inner flex flex-wrap items-center gap-3">
            <div className="mr-auto"><a className="font-semibold" href={`#/episodes/${e.id}`}>{t("episodes.episode", { n: e.number })} · {e.title}</a>
              <div className="muted text-sm">{t(profileLabel(e.profile))} · {t("episodes.target", { s: e.target_duration_s })} · {fmtTime(e.created_at, lang)}</div></div>
            {j && r?.state === "running" && <div className="w-40"><div className="mono mb-1 text-xs">{t(`stage.${j.stage}` as Key)}</div><Bar done={j.progress_done} total={j.progress_total} /></div>}
            {r && <StateBadge state={r.state} />}
            <a className="btn inline-flex items-center no-underline" href={`#/episodes/${e.id}`}>{t("episodes.open")}</a></li>; })}</ul>}
    </Card>
  </div>;
}
