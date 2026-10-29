import { useState } from "react";
import { api, type PasskeyList } from "../api";
import { usePoll } from "../hooks";
import { useI18n, type Key } from "../i18n";
import { Card, fmtTime } from "../ui";
import { createPasskey } from "../webauthn";
import { errorKey } from "./Login";

type Begin = { ceremony_id: string; options: never };

// Security lists the passkeys, adds another (needs the current session, no code) and removes one.
// The server refuses removing the last passkey; the button is disabled for it and says why.
export function Security() {
  const { t, lang } = useI18n();
  const list = usePoll<PasskeyList>("/auth/credentials", 10000);
  const creds = list.data?.credentials ?? [];
  const [label, setLabel] = useState("");
  const [busy, setBusy] = useState(false);
  const [confirming, setConfirming] = useState<number | null>(null);
  const [msg, setMsg] = useState<{ ok: boolean; key: Key } | null>(null);

  const add = async () => {
    setBusy(true); setMsg(null);
    try {
      const b = await api<Begin>("/auth/enroll/begin", { method: "POST", body: {} });
      const credential = await createPasskey(b.options);
      await api("/auth/enroll/finish", { method: "POST", body: { ceremony_id: b.ceremony_id, label: label.trim() || t("auth.labelDefault"), credential } });
      setLabel(""); setMsg({ ok: true, key: "security.added" }); list.reload();
    } catch (e) { setMsg({ ok: false, key: errorKey(e) }); } finally { setBusy(false); }
  };
  const remove = async (id: number) => {
    setBusy(true); setMsg(null);
    try {
      await api(`/auth/credentials/${id}`, { method: "DELETE" });
      setConfirming(null); setMsg({ ok: true, key: "security.removed" }); list.reload();
    } catch { setMsg({ ok: false, key: "security.removeFailed" }); } finally { setBusy(false); }
  };

  return <div className="flex flex-col gap-4">
    <Card><h1 className="font-display text-2xl font-extrabold">{t("security.title")}</h1><p className="muted">{t("security.sessions")}</p></Card>
    <Card title={t("security.passkeys")}>
      <p className="muted mb-3">{t("security.passkeysHelp")}</p>
      {!list.data ? <p className="muted">{t("app.loading")}</p> : <ul className="flex flex-col gap-2">
        {creds.map((c) => <li key={c.id} className="inner flex flex-wrap items-center gap-3">
          <div className="mr-auto"><div className="font-semibold">{c.label}{c.id === list.data?.current_credential_id && <span className="mono ml-2 text-sm text-green">● {t("security.thisOne")}</span>}</div>
            <div className="muted mono text-sm">{t("security.created")} {fmtTime(c.created_at, lang)} · {t("security.lastUsed")} {c.last_used_at ? fmtTime(c.last_used_at, lang) : t("security.never")}</div></div>
          {creds.length <= 1 ? <span className="muted text-sm">{t("security.lastOneHint")}</span>
            : confirming === c.id
              ? <><button className="btn btn-danger" disabled={busy} onClick={() => void remove(c.id)}>{busy ? t("security.removing") : t("security.removeConfirm")}</button><button className="btn" disabled={busy} onClick={() => setConfirming(null)}>{t("app.cancel")}</button></>
              : <button className="btn btn-danger" disabled={busy} onClick={() => setConfirming(c.id)}>{t("security.remove")}</button>}
        </li>)}
      </ul>}
    </Card>
    <Card title={t("security.add")}>
      <form className="flex flex-wrap items-end gap-3" onSubmit={(e) => { e.preventDefault(); void add(); }}>
        <div className="min-w-60 flex-1"><label htmlFor="add-label" className="mb-1 block text-sm">{t("auth.label")}</label>
          <input id="add-label" className="field" value={label} maxLength={80} placeholder={t("auth.labelDefault")} onChange={(e) => setLabel(e.target.value)} /></div>
        <button className="btn btn-primary" disabled={busy}>{busy ? t("auth.enrollBusy") : t("security.addBtn")}</button>
      </form>
      {msg && <p role={msg.ok ? "status" : "alert"} className={`mt-3 ${msg.ok ? "text-green" : "text-red"}`}>{t(msg.key)}</p>}
    </Card>
  </div>;
}
