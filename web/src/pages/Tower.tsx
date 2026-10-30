import "../tower.css";
import { useEffect, useRef, useState } from "react";
import { useDocumentHidden, usePoll, useReducedMotion } from "../hooks";
import { useI18n, type Key } from "../i18n";
import { Tower as TowerScene, LampGlyph } from "../TowerScene";
import { AgentDetail, PipelinePanel, ProgressTracker, RightColumn } from "../TowerPanels";
import { carFloor, motionOf, pollInterval, type FloorState, type TowerPayload } from "../tower";
import { Card, fmtTime } from "../ui";

const LEGEND: FloorState[] = ["done", "active", "idle", "waiting", "failed", "not_implemented"];

// TowerPage loads GET /api/tower and hands the payload to the scene. The page is optional: nothing else
// depends on it. A failed or unreachable backend freezes the scene on the last real payload (or shows none).
export function TowerPage() {
  const { t, lang } = useI18n();
  const hidden = useDocumentHidden();
  const reduced = useReducedMotion();
  const [seen, setSeen] = useState<TowerPayload | null>(null); // last payload and link state, to choose the next interval
  const [down, setDown] = useState(false);
  const q = usePoll<TowerPayload>("/tower", pollInterval(seen?.tower ?? null, { hidden, stale: down }));
  const [selected, setSelected] = useState<string | null>(null);
  const stale = q.disconnected || q.error !== null;
  const motion = motionOf(q.data?.tower ?? { floors: [], active_episode: null }, { stale, reduced });
  useEffect(() => { setSeen(q.data); }, [q.data]);
  useEffect(() => { setDown(stale); }, [stale]);
  const wasHidden = useRef(hidden);
  const { reload } = q;
  useEffect(() => { if (wasHidden.current && !hidden) reload(); wasHidden.current = hidden; }, [hidden]); // eslint-disable-line react-hooks/exhaustive-deps
  const select = (c: string) => setSelected((s) => (s === c ? null : c));
  const car = q.data ? carFloor(q.data.tower) : null;

  return <div className="flex flex-col gap-4">
    <h1 className="m-0 font-display text-2xl font-extrabold">{t("menu.tower")}</h1>
    {q.disconnected && <div role="alert" className="card border-red text-red"><strong>{t("net.disconnected")}</strong> <span className="muted">{t("tower.frozen")}</span></div>}
    {q.error && !q.disconnected && <div role="alert" className="card border-red text-red"><strong>{t("tower.loadError")}</strong> <button className="btn ml-2" onClick={q.reload}>{t("tower.reload")}</button></div>}
    {!q.data && !q.error && <div role="status" className="card muted">{t("app.loading")}</div>}
    {q.data && <div className="grid items-start gap-4 2xl:grid-cols-[minmax(0,1fr)_340px]">
      <div className="flex min-w-0 flex-col gap-4">
        <Card className={`flex overflow-x-auto !p-2 ${hidden ? "tower-paused" : ""}`}>
          <ProgressTracker floors={q.data.tower.floors} frozen={stale} motion={motion.enabled} />
          <TowerScene tower={q.data.tower} motion={motion} frozen={stale} selected={selected} onSelect={select} />
        </Card>
        <div className={stale ? "tower-frozen" : ""} data-frozen={stale || undefined}>
          <AgentDetail floor={q.data.tower.floors.find((f) => f.code === selected) ?? null} onClear={() => setSelected(null)} />
        </div>
        <PipelinePanel ep={q.data.tower.active_episode} frozen={stale} />
      </div>
      <RightColumn tower={q.data.tower} selected={selected} onSelect={select} frozen={stale} />
    </div>}
    {q.data && <div className="flex flex-wrap items-center gap-x-5 gap-y-2 text-sm">
      <ul aria-label={t("tower.legend")} className="m-0 flex list-none flex-wrap gap-x-4 gap-y-1 p-0">
        {LEGEND.map((s) => <li key={s} className="mono flex items-center gap-1.5"><svg aria-hidden width="18" height="18" viewBox="-12 -12 24 24"><LampGlyph state={s} /></svg>{t(`tower.state.${s}` as Key)}</li>)}
      </ul>
      <span className="muted">{car ? t("tower.carAt", { floor: car }) : t("tower.carRest")}</span>
      <span className="muted mono">{t("tower.updated", { time: fmtTime(q.data.generated_at, lang) })}</span>
    </div>}
  </div>;
}
