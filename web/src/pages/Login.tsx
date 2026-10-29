import { useState, type FormEvent } from "react";
import { api, ApiError, type AuthStatus } from "../api";
import { useI18n, type Key } from "../i18n";
import { Card, LangSwitch } from "../ui";
import { createPasskey, getPasskey, wasCancelled, webauthnSupported } from "../webauthn";

type Begin = { ceremony_id: string; options: never };

// errorKey maps a failure to a fixed message. The server never says why a sign-in failed, and neither do we.
export function errorKey(e: unknown): Key {
  if (wasCancelled(e)) return "auth.err.cancelled";
  if (e instanceof DOMException && e.name === "InvalidStateError") return "auth.err.duplicate"; // this authenticator already holds a passkey for the app
  if (e instanceof ApiError) {
    if (e.status === 0) return "auth.err.network";
    if (e.status === 401) return "auth.err.failed";
    if (e.status === 403) return "auth.err.already";
    if (e.status === 429) return "auth.err.tooMany";
  }
  return "auth.err.other";
}

// LoginScreen is shown whenever there is no valid session. Before the first passkey exists it is the
// enrollment form (needs the one-time code from `server enroll`); afterwards it is the sign-in button.
export function LoginScreen({ status, expired, onDone }: { status: AuthStatus; expired: boolean; onDone: () => void }) {
  const { t } = useI18n();
  const supported = webauthnSupported();
  return <div className="mx-auto flex min-h-screen max-w-[520px] flex-col justify-center gap-4 p-4">
    <header className="flex items-center justify-between gap-3">
      <div><div className="font-display text-xl font-extrabold">{t("app.name")}</div><div className="muted text-sm">{t("app.tagline")}</div></div>
      <LangSwitch />
    </header>
    {expired && <div role="alert" className="card border-amber">{t("auth.expired")}</div>}
    {!supported && <div role="alert" className="card border-red text-red">{t("auth.unsupported")}</div>}
    {status.enrolled ? <SignIn disabled={!supported} onDone={onDone} /> : <Enroll disabled={!supported} onDone={onDone} />}
  </div>;
}

function SignIn({ disabled, onDone }: { disabled: boolean; onDone: () => void }) {
  const { t } = useI18n();
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<Key | null>(null);
  const run = async () => {
    setBusy(true); setErr(null);
    try {
      const b = await api<Begin>("/auth/login/begin", { method: "POST", body: {} });
      const credential = await getPasskey(b.options);
      await api("/auth/login/finish", { method: "POST", body: { ceremony_id: b.ceremony_id, credential } });
      onDone();
    } catch (e) { setErr(errorKey(e)); } finally { setBusy(false); }
  };
  return <Card title={t("auth.loginTitle")}>
    <p className="muted mb-3">{t("auth.loginHelp")}</p>
    <button className="btn btn-primary" disabled={disabled || busy} onClick={() => void run()}>{busy ? t("auth.loginBusy") : t("auth.loginBtn")}</button>
    {err && <p role="alert" className="mt-3 text-red">{t(err)}</p>}
  </Card>;
}

function Enroll({ disabled, onDone }: { disabled: boolean; onDone: () => void }) {
  const { t } = useI18n();
  const [code, setCode] = useState("");
  const [label, setLabel] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<Key | null>(null);
  const submit = async (ev: FormEvent) => {
    ev.preventDefault();
    setBusy(true); setErr(null);
    try {
      const b = await api<Begin>("/auth/enroll/begin", { method: "POST", body: { code } });
      const credential = await createPasskey(b.options);
      await api("/auth/enroll/finish", { method: "POST", body: { ceremony_id: b.ceremony_id, label: label.trim() || t("auth.labelDefault"), credential } });
      onDone();
    } catch (e) { setErr(errorKey(e)); } finally { setBusy(false); }
  };
  return <Card title={t("auth.enrollTitle")}>
    <form onSubmit={(e) => void submit(e)} className="flex flex-col gap-3">
      <p className="muted">{t("auth.enrollHelp", { cmd: t("auth.enrollCmd") })}</p>
      <div><label htmlFor="enroll-code" className="mb-1 block text-sm">{t("auth.code")}</label>
        <input id="enroll-code" className="field mono" value={code} autoComplete="off" autoCapitalize="characters" spellCheck={false} maxLength={40} required onChange={(e) => setCode(e.target.value)} /></div>
      <div><label htmlFor="enroll-label" className="mb-1 block text-sm">{t("auth.label")}</label>
        <input id="enroll-label" className="field" value={label} maxLength={80} placeholder={t("auth.labelDefault")} onChange={(e) => setLabel(e.target.value)} /></div>
      <div><button className="btn btn-primary" disabled={disabled || busy || code.trim() === ""}>{busy ? t("auth.enrollBusy") : t("auth.enrollBtn")}</button></div>
      {err && <p role="alert" className="text-red">{t(err)}</p>}
    </form>
  </Card>;
}
