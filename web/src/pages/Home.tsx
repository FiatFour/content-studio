import { contentUrl, type EpisodeSummary, type System } from "../api";
import { usePoll } from "../hooks";
import { useI18n, type Key } from "../i18n";
import { Bar, Card, StateBadge, agentColor } from "../ui";

export function Home({ sys }: { sys: System | null }) {
  const { t } = useI18n();
  const eps = usePoll<{ episodes: EpisodeSummary[] }>("/episodes", 3000);
  const list = eps.data?.episodes ?? [];
  const running = list.filter((e) => e.latest_run?.state === "running");
  const review = list.filter((e) => e.latest_run?.state === "ready_for_review");
  const failed = list.filter((e) => e.latest_run?.state === "failed");
  const tile = (label: string, n: number) => <div className="card"><div className="muted text-sm">{label}</div><div className="font-display text-4xl font-extrabold">{eps.data ? n : "…"}</div></div>;
  const gib = (mib?: number) => (mib == null ? t("app.na") : `${(mib / 1024).toFixed(1)} GB`);
  return <div className="flex flex-col gap-4">
    <Card><h1 className="font-display text-2xl font-extrabold">{t("home.title")}</h1><p className="muted">{t("home.banner")}</p></Card>
    <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
      {tile(t("home.tile.total"), list.length)}{tile(t("home.tile.running"), running.length)}{tile(t("home.tile.review"), review.length)}{tile(t("home.tile.failed"), failed.length)}
    </div>
    <div className="grid gap-4 lg:grid-cols-[2fr_1fr]">
      <Card title={t("home.active")}>
        {running.length === 0 ? <p className="muted">{t("home.noActive")}</p> : <div className="overflow-x-auto"><table className="w-full text-left">
          <tbody>{running.map((e) => { const j = e.latest_run!.jobs.find((x) => x.state === "running") ?? e.latest_run!.jobs.at(-1)!;
            return <tr key={e.id} className="border-t border-line"><td className="py-2"><a href={`#/episodes/${e.id}`} className="font-semibold">{e.title}</a></td>
              <td style={{ color: agentColor[j.stage] }}>{t(`stage.${j.stage}` as Key)}</td>
              <td className="w-40"><Bar done={j.progress_done} total={j.progress_total} /></td><td className="mono text-sm">{j.progress_total > 0 ? `${j.progress_done}/${j.progress_total}` : ""}</td><td><StateBadge state={j.state} /></td></tr>; })}</tbody></table></div>}
      </Card>
      <Card title={t("home.machine")}>
        <dl className="grid grid-cols-2 gap-2 mono text-sm">
          <dt className="muted">{t("home.vram")}</dt><dd>{gib(sys?.vram_used_mib)} / {gib(sys?.vram_total_mib)}</dd>
          <dt className="muted">{t("home.ram")}</dt><dd>{gib(sys?.ram_used_mib)} / {gib(sys?.ram_total_mib)}</dd>
          <dt className="muted">{t("home.disk")}</dt><dd>{sys?.disk_free_gib != null ? `${sys.disk_free_gib.toFixed(0)} GiB` : t("app.na")}</dd>
        </dl>
      </Card>
    </div>
    <div className="grid gap-4 lg:grid-cols-3">
      <Card title={t("home.waiting")}>{review.length === 0 ? <p className="muted">{t("home.noWaiting")}</p> : <ul className="flex flex-col gap-2">{review.map((e) => <li key={e.id}><a href="#/review" className="font-semibold">{e.title}</a></li>)}</ul>}</Card>
      <ExportsCard />
      <Card title={t("home.failures")}>{failed.length === 0 ? <p className="muted">{t("home.noFailures")}</p> : <ul className="flex flex-col gap-2">{failed.map((e) => { const j = e.latest_run!.jobs.find((x) => x.state === "failed");
        return <li key={e.id}><a href={`#/episodes/${e.id}`} className="font-semibold">{e.title}</a><div className="mono text-sm text-red">{j?.stage}: {j?.error_class}</div></li>; })}</ul>}</Card>
    </div>
  </div>;
}

function ExportsCard() {
  const { t } = useI18n();
  const eps = usePoll<{ episodes: EpisodeSummary[] }>("/episodes", 10000);
  void contentUrl;
  return <Card title={t("home.exports")}><p className="muted">{eps.data ? t("home.noExports") : t("app.loading")}</p></Card>;
}
