import { useState, type FormEvent } from "react";
import { api, ApiError, type Character, type CharacterStatus } from "../api";
import { usePoll } from "../hooks";
import { useI18n, type Key } from "../i18n";
import { Card } from "../ui";

const statuses: CharacterStatus[] = ["alive", "deceased", "moved_away"];
type Form = Omit<Character, "id">;
const empty: Form = { slug: "", name: "", appearance: "", personality: "", voice: "", arrives_on: null, status: "alive", status_since: null, lora_name: "", trigger_token: "" };

export function Characters() {
  const { t } = useI18n();
  const list = usePoll<{ characters: Character[] }>("/characters", 5000);
  const [editing, setEditing] = useState<Character | null>(null);
  return <div className="flex flex-col gap-4">
    <Card title={editing ? t("characters.edit", { name: editing.name }) : t("characters.new")}>
      <CharacterForm key={editing?.id ?? "new"} initial={editing} onDone={() => { setEditing(null); list.reload(); }} onCancel={() => setEditing(null)} />
    </Card>
    <Card title={t("characters.title")}>
      <p className="muted mb-3 text-sm">{t("characters.intro")}</p>
      {!list.data ? <p className="muted">{t("app.loading")}</p> : list.data.characters.length === 0 ? <p className="muted">{t("characters.none")}</p> :
        <ul className="flex flex-col gap-3">{list.data.characters.map((c) => <li key={c.id} className="inner flex flex-wrap items-start gap-3">
          <div className="mr-auto min-w-0">
            <div><span className="font-semibold">{c.name}</span> <span className="mono muted text-sm">{c.slug}</span></div>
            <div className="muted text-sm">{c.status_since ? t("characters.since", { status: t(`characters.status.${c.status}` as Key), date: c.status_since }) : t(`characters.status.${c.status}` as Key)}{c.arrives_on ? ` · ${t("characters.arrives", { date: c.arrives_on })}` : ""}</div>
            <p className="mt-1 text-sm">{c.appearance}</p>
            {c.personality && <p className="muted text-sm">{c.personality}</p>}
            <p className="muted text-sm">{c.voice}</p>
            {(c.lora_name || c.trigger_token) && <p className="mono muted text-xs">{c.lora_name} {c.trigger_token}</p>}
          </div>
          <button className="btn" onClick={() => setEditing(c)}>{t("characters.editBtn")}</button></li>)}</ul>}
    </Card>
  </div>;
}

function CharacterForm({ initial, onDone, onCancel }: { initial: Character | null; onDone: () => void; onCancel: () => void }) {
  const { t } = useI18n();
  const [f, setF] = useState<Form>(initial ?? empty);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [saved, setSaved] = useState(false);
  const set = <K extends keyof Form>(k: K, v: Form[K]) => { setF((p) => ({ ...p, [k]: v })); setSaved(false); };
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setErr(""); setSaved(false);
    const body = { ...f, arrives_on: f.arrives_on ?? "", status_since: f.status === "alive" ? "" : f.status_since ?? "" };
    try {
      if (initial) await api(`/characters/${initial.id}`, { method: "PUT", body });
      else { await api("/characters", { method: "POST", body }); setF(empty); }
      setSaved(true); onDone();
    } catch (x) { setErr(x instanceof ApiError ? x.message : t("app.error")); }
    finally { setBusy(false); }
  };
  const text = (id: string, k: "slug" | "name" | "voice" | "lora_name" | "trigger_token", label: string, extra: { max: number; required?: boolean; disabled?: boolean; hint?: string }) =>
    <div><label htmlFor={id} className="mb-1 block text-sm">{label}</label>
      <input id={id} className="field" value={f[k]} maxLength={extra.max} required={extra.required} disabled={extra.disabled} onChange={(e) => set(k, e.target.value)} />
      {extra.hint && <p className="muted mt-1 text-xs">{extra.hint}</p>}</div>;
  return <form onSubmit={(e) => void submit(e)} className="grid gap-3 md:grid-cols-2">
    {text("ch-slug", "slug", t("characters.form.slug"), { max: 16, required: true, disabled: initial !== null, hint: t("characters.form.slugHint") })}
    {text("ch-name", "name", t("characters.form.name"), { max: 60, required: true })}
    <div className="md:col-span-2"><label htmlFor="ch-app" className="mb-1 block text-sm">{t("characters.form.appearance")}</label>
      <textarea id="ch-app" className="field" rows={3} value={f.appearance} minLength={5} maxLength={600} required onChange={(e) => set("appearance", e.target.value)} /></div>
    <div className="md:col-span-2"><label htmlFor="ch-pers" className="mb-1 block text-sm">{t("characters.form.personality")}</label>
      <textarea id="ch-pers" className="field" rows={2} value={f.personality} maxLength={600} onChange={(e) => set("personality", e.target.value)} /></div>
    <div className="md:col-span-2">{text("ch-voice", "voice", t("characters.form.voice"), { max: 200, required: true })}</div>
    <div><label htmlFor="ch-arr" className="mb-1 block text-sm">{t("characters.form.arrivesOn")}</label>
      <input id="ch-arr" type="date" className="field mono" value={f.arrives_on ?? ""} onChange={(e) => set("arrives_on", e.target.value || null)} /></div>
    <div><label htmlFor="ch-status" className="mb-1 block text-sm">{t("characters.form.status")}</label>
      <select id="ch-status" className="field" value={f.status} onChange={(e) => set("status", e.target.value as CharacterStatus)}>
        {statuses.map((s) => <option key={s} value={s}>{t(`characters.status.${s}` as Key)}</option>)}</select></div>
    {f.status !== "alive" && <div><label htmlFor="ch-since" className="mb-1 block text-sm">{t("characters.form.statusSince")}</label>
      <input id="ch-since" type="date" className="field mono" required value={f.status_since ?? ""} onChange={(e) => set("status_since", e.target.value || null)} />
      <p className="muted mt-1 text-xs">{t("characters.form.statusSinceHint")}</p></div>}
    <div className="grid gap-3 md:col-span-2 md:grid-cols-2">
      {text("ch-lora", "lora_name", t("characters.form.lora"), { max: 100 })}
      {text("ch-trig", "trigger_token", t("characters.form.trigger"), { max: 60 })}
      <p className="muted text-xs md:col-span-2">{t("characters.form.loraHelp")} <span className="mono">{t("characters.form.refImages")}</span></p>
    </div>
    {err && <p role="alert" className="text-red md:col-span-2">{err}</p>}
    {saved && <p role="status" className="text-green md:col-span-2">{t("characters.saved")}</p>}
    <div className="flex gap-2 md:col-span-2">
      <button className="btn btn-primary" disabled={busy}>{busy ? t("characters.form.saving") : initial ? t("characters.form.save") : t("characters.form.create")}</button>
      {initial && <button type="button" className="btn" onClick={onCancel}>{t("characters.form.cancel")}</button>}
    </div>
  </form>;
}
