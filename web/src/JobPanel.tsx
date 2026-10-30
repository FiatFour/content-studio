import { type JobInspection } from "./api";
import { usePoll } from "./hooks";
import { dictionaries, useI18n, type Key } from "./i18n";
import { Card, StateBadge, fmtTime } from "./ui";

// ErrorExplain shows the static TH/EN explanation of an error class (no model involved). A class the
// table does not know is said to be unexplained rather than guessed at.
export function ErrorExplain({ errorClass }: { errorClass: string }) {
  const { t } = useI18n();
  if (dictionaries.en[`errclass.${errorClass}.what`] === undefined) return <p className="muted mt-1 text-sm">{t("job.unknownClass")}</p>;
  return <div className="mt-1 text-sm">
    <p><strong>{t("job.explainWhat")}:</strong> {t(`errclass.${errorClass}.what` as Key)}</p>
    <p><strong>{t("job.explainNext")}:</strong> {t(`errclass.${errorClass}.next` as Key)}</p>
  </div>;
}

// JobPanel is the read-only inspection of one job: everything comes from GET /api/jobs/:id.
export function JobPanel({ jobId, onClose }: { jobId: number; onClose: () => void }) {
  const { t, lang } = useI18n();
  const d = usePoll<JobInspection>(`/jobs/${jobId}`, 3000);
  const j = d.data;
  const none = <span className="muted">{t("job.notRecorded")}</span>;
  const when = (s: string | null) => (s ? fmtTime(s, lang) : null);
  const row = (label: string, value: React.ReactNode) => <div className="inner"><dt className="muted text-xs">{label}</dt><dd className="mono text-sm break-words">{value}</dd></div>;
  return <Card title={<span className="flex flex-wrap items-center gap-3">{t("job.title")}{j && <> · {t(`stage.${j.stage}` as Key)} <StateBadge state={j.state} /></>}
      <button className="btn ml-auto" onClick={onClose}>{t("job.close")}</button></span>}>
    {!j ? (d.error ? <p role="alert" className="text-red">{t("job.loadFailed")}</p> : <p>{t("app.loading")}</p>) : <>
      <dl className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
        {row(t("episode.attempt", { n: j.attempt }), t("job.attemptsOf", { n: j.attempt, max: j.max_attempts }))}
        {row(t("job.provider"), j.provider ?? none)}
        {row(t("job.model"), j.model ?? none)}
        {row(t("job.lease"), j.state !== "running" ? <span className="muted">{t("job.leaseNone")}</span> : j.lease_expires_at === null ? none
          : j.lease_expired ? t("job.leaseExpired", { t: when(j.lease_expires_at) ?? "" }) : t("job.leaseOk", { t: when(j.lease_expires_at) ?? "" }))}
        {row(t("job.heartbeat"), when(j.heartbeat_at) ?? none)}
        {j.run_after && row(t("job.retryAt"), when(j.run_after))}
        {row(t("job.execution"), j.execution ? t("job.executionRow", { h: j.execution.input_hash, n: j.execution.attempt, t: when(j.execution.at) ?? "" }) : <span className="muted">{t("job.executionNone")}</span>)}
        {row(t("job.cloud"), <span className="muted">{t("job.cloudNone")}</span>)}
      </dl>
      <div className="inner mt-3"><strong>{t("job.resume")}:</strong> <span className="mono">{t(`resume.${j.resume.verdict}` as Key)}</span>
        <p className="muted text-sm">{t(`resume.reason.${j.resume.reason}` as Key)}</p></div>
      {j.error_class && <div role="alert" className="mt-3 text-red">
        <p><strong>{t("job.errorClass")}:</strong> <span className="mono">{j.error_class}</span></p>
        {j.error_message && <p><strong>{t("job.errorText")}:</strong> {j.error_message}</p>}
        <ErrorExplain errorClass={j.error_class} />
      </div>}
    </>}
  </Card>;
}
