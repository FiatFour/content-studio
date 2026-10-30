import { useEffect, useState } from "react";
import { api, contentUrl } from "../api";
import { usePoll } from "../hooks";
import { useI18n, type Key } from "../i18n";
import { Card, fmtBytes, fmtTime } from "../ui";

type HistoryRow = {
  id: number; run_id: number; type: string; item_key: string; version: number; sha256: string; size_bytes: number;
  validation_status: string; provider: string; model: string; created_at: string; parents: number[]; approval: string;
};
type Side = { id: number; type: string; item_key: string; version: number; sha256: string; size_bytes: number; created_at: string; approval: string };
type Diff = { path: string; op: "added" | "removed" | "changed"; before?: string; after?: string };
type Compare = { a: Side; b: Side; identical: boolean; kind: "json" | "image" | "video" | "audio" | "text"; diffs?: Diff[]; truncated?: boolean };

// Everything here is read-only. Artifact content is untrusted: it is only ever placed in the page as text
// (React escapes it) or as the source of an img/video/audio element served with a fixed content type.

export function ArtifactHistory({ episodeId }: { episodeId: number }) {
  const { t, lang } = useI18n();
  const h = usePoll<{ artifacts: HistoryRow[]; truncated: boolean }>(`/episodes/${episodeId}/artifact-history`, 5000);
  const rows = h.data?.artifacts ?? [];
  // rows are newest first, so the previous version of a row is the next later row of the same type and item.
  const previous = (r: HistoryRow) => rows.find((x) => x.type === r.type && x.item_key === r.item_key && x.version < r.version);
  return <Card title={<>{t("history.title")} <a className="ml-3 text-sm" href={`#/episodes/${episodeId}`}>{t("history.back")}</a></>}>
    {!h.data ? <p className="muted">{h.error ? t("app.error") : t("app.loading")}</p> : rows.length === 0 ? <p className="muted">{t("history.empty")}</p> :
      <div className="overflow-auto"><table className="w-full text-left mono text-xs">
        <thead><tr className="muted"><th>{t("history.id")}</th><th>{t("history.type")}</th><th>{t("history.version")}</th><th>sha256</th><th>{t("history.parents")}</th><th>{t("history.approval")}</th><th>{t("history.created")}</th><th /></tr></thead>
        <tbody>{rows.map((r) => { const p = previous(r); return <tr key={r.id} className="border-t border-line align-top">
          <td className="py-1">#{r.id}</td>
          <td>{r.type}{r.item_key ? `/${r.item_key}` : ""}</td>
          <td>v{r.version}</td>
          <td title={r.sha256}>{r.sha256.slice(0, 12)}</td>
          <td>{r.parents.length === 0 ? "-" : r.parents.map((x) => `#${x}`).join(", ")}</td>
          <td className={r.approval === "approved" ? "text-green" : "muted"}>{t(`review.status.${r.approval}` as Key)}</td>
          <td className="muted">{fmtTime(r.created_at, lang)} · {fmtBytes(r.size_bytes)}</td>
          <td>{p && <a href={`#/compare/${p.id}/${r.id}`}>{t("history.compareWithPrev")}</a>}</td></tr>; })}</tbody></table>
        {h.data.truncated && <p className="muted mt-2 text-xs">{t("history.truncated")}</p>}</div>}
  </Card>;
}

function SideText({ id }: { id: number }) {
  const { t } = useI18n();
  const [text, setText] = useState<string | null>(null);
  useEffect(() => {
    let live = true;
    setText(null);
    fetch(contentUrl(id)).then((r) => r.text()).then((x) => live && setText(x.slice(0, 100_000))).catch(() => live && setText(""));
    return () => { live = false; };
  }, [id]);
  return <pre className="inner max-h-96 overflow-auto whitespace-pre-wrap text-sm">{text ?? t("app.loading")}</pre>;
}

function Media({ kind, id }: { kind: Compare["kind"]; id: number }) {
  const src = contentUrl(id);
  if (kind === "image") return <img src={src} alt="" loading="lazy" className="max-h-[60vh] w-full rounded-lg border-2 border-outline object-contain" />;
  if (kind === "video") return <video controls playsInline preload="metadata" src={src} className="max-h-[60vh] w-full rounded-xl border-2 border-outline bg-black" />;
  if (kind === "audio") return <audio controls preload="metadata" src={src} className="w-full" />;
  return <SideText id={id} />;
}

function SideHead({ s }: { s: Side }) {
  const { t, lang } = useI18n();
  return <div className="mb-2">
    <div className="font-display font-extrabold">{s.type}{s.item_key ? `/${s.item_key}` : ""} · {t("episode.version", { n: s.version })} <span className="muted mono text-xs">#{s.id}</span></div>
    <div className="mono break-all text-xs">{s.sha256}</div>
    <div className="muted text-xs">{fmtTime(s.created_at, lang)} · {fmtBytes(s.size_bytes)} · {t(`review.status.${s.approval}` as Key)}</div>
  </div>;
}

export function ArtifactCompare({ a, b }: { a: number; b: number }) {
  const { t } = useI18n();
  const [data, setData] = useState<Compare | null>(null);
  const [err, setErr] = useState("");
  useEffect(() => {
    let live = true;
    setData(null); setErr("");
    api<Compare>(`/artifacts/compare?a=${a}&b=${b}`).then((x) => live && setData(x)).catch((e: unknown) => live && setErr(e instanceof Error ? e.message : t("app.error")));
    return () => { live = false; };
  }, [a, b, t]);
  if (err) return <Card><p role="alert" className="text-red">{err}</p></Card>;
  if (!data) return <Card>{t("app.loading")}</Card>;
  return <div className="flex flex-col gap-4">
    <Card title={t("compare.title")}>
      <p className="muted text-sm">{t("compare.readOnly")}</p>
      {data.identical && <p className="mt-2 text-sm">{t("compare.identical")}</p>}
    </Card>
    <div className="grid gap-4 md:grid-cols-2">
      <Card><SideHead s={data.a} />{data.kind === "json" ? null : <Media kind={data.kind} id={data.a.id} />}</Card>
      <Card><SideHead s={data.b} />{data.kind === "json" ? null : <Media kind={data.kind} id={data.b.id} />}</Card>
    </div>
    {data.kind === "json" && <Card title={t("compare.changes")}>
      {(data.diffs ?? []).length === 0 ? <p className="muted">{t("compare.noChanges")}</p> :
        <ul className="flex flex-col gap-2">{data.diffs!.map((d) => <li key={d.path} className="inner mono text-xs">
          <div><strong className={d.op === "added" ? "text-green" : d.op === "removed" ? "text-red" : "text-amber"}>{t(`compare.op.${d.op}` as Key)}</strong> <span className="break-all">{d.path || "/"}</span></div>
          {d.before !== undefined && <pre className="whitespace-pre-wrap break-all text-red">- {d.before}</pre>}
          {d.after !== undefined && <pre className="whitespace-pre-wrap break-all text-green">+ {d.after}</pre>}</li>)}</ul>}
      {data.truncated && <p className="muted mt-2 text-xs">{t("compare.truncated")}</p>}
    </Card>}
  </div>;
}
