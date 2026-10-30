import { dictionaries, useI18n, type Key } from "./i18n";
import { agentColour, countText, isKnownState, pipelineSteps, reviewHref, teamRows, trackerDots, visualOf, type DotKind, type EpisodeStatus, type Floor, type TowerState } from "./tower";
import { Bar, Card, StateBadge, fmtTime } from "./ui";

// One place so every panel prints agent and stage names the same way and never shows a raw key.
function useNames() {
  const { t, lang } = useI18n();
  const has = (k: string) => k in dictionaries[lang];
  return {
    t, lang,
    agent: (a: string) => (has(`tower.agent.${a}`) ? t(`tower.agent.${a}` as Key) : a),
    stage: (s: string) => (has(`tower.stage.${s}`) ? t(`tower.stage.${s}` as Key) : s),
    state: (s: string) => (isKnownState(s) ? t(`tower.state.${s}` as Key) : s),
  };
}

const DOT_STYLE: Record<DotKind, string> = {
  check: "border-blue bg-blue text-bg",
  pulse: "tower-pulse border-green bg-green",
  hollow: "border-muted bg-transparent",
  failed: "border-badge-red bg-badge-red text-bg",
  waiting: "border-amber bg-transparent",
};
const DOT_MARK: Record<DotKind, string> = { check: "✓", pulse: "", hollow: "", failed: "×", waiting: "‖" };

// ProgressTracker sits beside the scene and is positioned from the same floor geometry, so each dot
// lines up with its floor. The line segment under a dot is solid once the floor below is finished.
export function ProgressTracker({ floors, frozen }: { floors: Floor[]; frozen: boolean }) {
  const n = useNames();
  const dots = trackerDots(floors);
  return <div role="group" aria-label={n.t("tower.progress")} title={n.t("tower.progressDesc")} className={`relative w-12 shrink-0 ${frozen ? "tower-frozen" : ""}`}>
    {dots.map((d, i) => {
      const above = dots[i + 1];
      return <div key={d.code}>
        {above && <span aria-hidden className="absolute left-1/2 w-0 -translate-x-1/2 border-l-[3px]"
          style={{ top: `${above.yPct}%`, height: `${d.yPct - above.yPct}%`, borderLeftStyle: above.solidBelow ? "solid" : "dashed", borderColor: above.solidBelow ? "var(--color-blue)" : "var(--color-muted)" }} />}
        <span data-dot={d.code} data-kind={d.kind} role="img" aria-label={`${d.code}: ${n.state(d.state)}`}
          className={`absolute left-1/2 grid h-6 w-6 -translate-x-1/2 -translate-y-1/2 place-items-center rounded-full border-[3px] text-xs font-extrabold ${DOT_STYLE[d.kind]}`}
          style={{ top: `${d.yPct}%` }}>{DOT_MARK[d.kind]}</span>
      </div>;
    })}
  </div>;
}

const Row = ({ label, children }: { label: string; children: React.ReactNode }) =>
  <div className="flex flex-wrap gap-x-2"><dt className="muted">{label}</dt><dd className="m-0 min-w-0 break-words font-semibold">{children}</dd></div>;

// AgentDetail shows what the payload says about one floor. A missing value reads "no data"; nothing is guessed.
export function AgentDetail({ floor, onClear }: { floor: Floor | null; onClear: () => void }) {
  const n = useNames();
  const { t } = n;
  if (!floor) return <Card title={t("tower.detail")}><p className="muted m-0">{t("tower.detail.pick")}</p></Card>;
  const none = <span className="muted font-normal">{t("tower.detail.noData")}</span>;
  const agentOr = (a: string | null) => (a ? n.agent(a) : none);
  const task = floor.episodes.length === 0 ? <span className="muted font-normal">{t("tower.detail.noTask")}</span>
    : floor.episodes.map((e) => <span key={e.id} className="mr-3 inline-block">{`#${e.number} ${e.title}`}{floor.progress ? ` (${floor.progress.done}/${floor.progress.total})` : ""}</span>);
  return <Card className="text-sm" title={<span className="flex items-center gap-2">
    <span aria-hidden className="inline-block h-3 w-3 rounded-full" style={{ background: agentColour[floor.agent] }} />
    <span>{n.agent(floor.agent)}</span><span className="mono muted text-sm font-normal">{floor.code === "Roof" ? t("tower.roof") : floor.code}</span>
    <button type="button" className="btn ml-auto !px-3 text-sm" onClick={onClear}>{t("tower.detail.close")}</button></span>}>
    <div className="mb-2"><StatusLine floor={floor} /></div>
    <dl data-agent-detail={floor.code} className="m-0 grid gap-1.5">
      <Row label={t("tower.detail.from")}>{agentOr(floor.handoff_from)}</Row>
      <Row label={t("tower.detail.to")}>{agentOr(floor.handoff_to)}</Row>
      <Row label={t("tower.detail.task")}>{task}</Row>
      <Row label={t("tower.detail.provider")}>{floor.provider ?? none}</Row>
      <Row label={t("tower.detail.model")}>{floor.model ?? none}</Row>
      <Row label={t("tower.detail.log")}>{floor.last_log ? <span className="mono">{floor.last_log}</span> : none}</Row>
    </dl>
  </Card>;
}

// The floor's own state word (not a job state), in its lamp colour.
function StatusLine({ floor }: { floor: Floor }) {
  const n = useNames();
  const v = visualOf(floor.state);
  return <span className="mono text-sm" style={{ color: v.color }}>{n.state(floor.state)}</span>;
}

function EpisodeRow({ e }: { e: EpisodeStatus }) {
  const { t } = useI18n();
  return <li className="inner" data-run={e.run_id}>
    <div className="mb-1.5 flex flex-wrap items-center justify-between gap-2"><span className="font-semibold">{e.number !== undefined ? `#${e.number} ` : ""}{e.title ?? ""}</span><StateBadge state={e.run_state} /></div>
    <Bar done={e.stages.done} total={e.stages.total} />
    <div className="muted mono mt-1 text-xs">{t("tower.today.stages", { done: e.stages.done, total: e.stages.total })}</div>
  </li>;
}

export function RightColumn({ tower, selected, onSelect, frozen }: { tower: TowerState; selected: string | null; onSelect: (code: string) => void; frozen: boolean }) {
  const n = useNames();
  const { t, lang } = n;
  const clip = tower.awaiting_review;
  return <div className={`flex flex-col gap-4 ${frozen ? "tower-frozen" : ""}`} data-frozen={frozen || undefined}>
    <Card title={t("tower.today")}>
      {tower.today.length === 0 ? <p className="muted m-0">{t("tower.today.none")}</p>
        : <ul className="m-0 flex list-none flex-col gap-2 p-0">{tower.today.map((e) => <EpisodeRow key={e.run_id} e={e} />)}</ul>}
    </Card>
    <Card title={t("tower.team")}>
      <ul className="m-0 flex list-none flex-col gap-1 p-0">
        {teamRows(tower.floors).map((r) => <li key={r.code}>
          <button type="button" aria-pressed={selected === r.code} onClick={() => onSelect(r.code)} data-team={r.code}
            className="btn flex w-full items-center gap-2 !px-3 text-left text-sm" style={selected === r.code ? { borderColor: "var(--color-blue)" } : undefined}>
            <span aria-hidden className="h-3 w-3 shrink-0 rounded-full" style={{ background: agentColour[r.agent] }} />
            <span className="min-w-0 flex-1 truncate font-semibold">{n.agent(r.agent)}</span>
            <span className="muted mono text-xs">{n.state(r.state)}</span>
            <span className="mono min-w-8 text-right font-bold" title={t("tower.team.tasks")} aria-label={`${t("tower.team.tasks")}: ${countText(r.tasks)}`}>{countText(r.tasks)}</span>
          </button>
        </li>)}
      </ul>
      <p className="muted m-0 mt-2 text-xs">{t("tower.team.tasks")}</p>
    </Card>
    <Card title={t("tower.awaiting")}>
      {clip ? <div className="inner" data-clip={clip.artifact_id}>
        <div className="font-semibold">{`#${clip.number} ${clip.title}`}</div>
        <div className="muted mono text-xs">{`v${clip.version} · ${fmtTime(clip.created_at, lang)}`}</div>
        <a className="btn mt-2 inline-flex items-center no-underline" href={reviewHref(clip)}>{t("tower.awaiting.open")}</a>
      </div> : <p className="muted m-0">{t("tower.awaiting.none")}</p>}
    </Card>
  </div>;
}

// PipelinePanel lists the active episode's stages in order. The last step is Export; nothing here publishes.
export function PipelinePanel({ ep, frozen }: { ep: EpisodeStatus | null; frozen: boolean }) {
  const n = useNames();
  const { t } = n;
  const steps = pipelineSteps(ep);
  return <Card title={t("tower.pipeline")} className={frozen ? "tower-frozen" : ""}>
    {steps.length === 0 ? <p className="muted m-0">{t("tower.pipeline.none")}</p> : <>
      {ep?.title && <p className="muted m-0 mb-2 text-sm">{`#${ep.number} ${ep.title}`}</p>}
      <ol aria-label={t("tower.pipeline")} className="m-0 grid list-none grid-cols-2 gap-2 p-0 sm:grid-cols-4">
        {steps.map((s, i) => <li key={s.stage} data-stage={s.stage} data-state={s.state} className="inner flex flex-col gap-1 text-sm">
          <span className="flex items-center gap-2"><span className="mono muted">{i + 1}</span><span className="font-semibold">{n.stage(s.stage)}</span></span>
          <StateBadge state={s.state} />
          {s.progress && s.progress.total > 0 && <Bar done={s.progress.done} total={s.progress.total} />}
        </li>)}
      </ol>
    </>}
  </Card>;
}
