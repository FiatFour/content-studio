import { useCallback, useEffect, useState } from "react";
import { api, ApiError, setUnauthorizedHandler, type AuthStatus, type System } from "./api";
import { usePoll } from "./hooks";
import { I18nProvider, useI18n, type Lang } from "./i18n";
import { Home } from "./pages/Home";
import { Episodes } from "./pages/Episodes";
import { Characters } from "./pages/Characters";
import { CanonProposalPage, CanonProposals } from "./pages/Canon";
import { EpisodeDetail } from "./pages/EpisodeDetail";
import { ReviewPage, ReviewQueue } from "./pages/Review";
import { ArtifactCompare, ArtifactHistory } from "./pages/History";
import { LoginScreen } from "./pages/Login";
import { Security } from "./pages/Security";
import { Providers } from "./pages/Providers";
import { TowerPage } from "./pages/Tower";
import { LangSwitch } from "./ui";

function useHash(): string[] {
  const [h, setH] = useState(location.hash);
  useEffect(() => { const f = () => setH(location.hash); addEventListener("hashchange", f); return () => removeEventListener("hashchange", f); }, []);
  return h.replace(/^#\/?/, "").split("/").filter(Boolean);
}

// Before sign-in the backend refuses /settings, so the language choice lives in this browser until then.
const storedLang = (): Lang => (localStorage.getItem("lang") === "en" ? "en" : "th");

export function App() {
  const [status, setStatus] = useState<AuthStatus | null>(null);
  const [unreachable, setUnreachable] = useState(false);
  const [expired, setExpired] = useState(false);
  const refresh = useCallback(async () => {
    try { setStatus(await api<AuthStatus>("/auth/status")); setUnreachable(false); } catch (e) { setUnreachable(e instanceof ApiError && (e.status === 0 || e.status >= 500)); }
  }, []);
  // Any 401 anywhere (an expired or revoked session) drops back to the login screen.
  useEffect(() => {
    setUnauthorizedHandler(() => { setExpired(true); setStatus((s) => (s ? { ...s, authenticated: false } : s)); });
    return () => setUnauthorizedHandler(null);
  }, []);
  useEffect(() => { void refresh(); }, [refresh]);
  useEffect(() => { if (!status) { const id = setInterval(() => void refresh(), 3000); return () => clearInterval(id); } }, [status, refresh]);
  const onLangLocal = useCallback((l: Lang) => localStorage.setItem("lang", l), []);

  if (!status) return <I18nProvider initial={storedLang()} onChange={onLangLocal}><Pending unreachable={unreachable} /></I18nProvider>;
  if (!status.authenticated) {
    return <I18nProvider initial={storedLang()} onChange={onLangLocal}>
      <LoginScreen status={status} expired={expired} onDone={() => { setExpired(false); void refresh(); }} />
    </I18nProvider>;
  }
  return <Authed onSignedOut={() => { setExpired(false); void refresh(); }} />;
}

function Pending({ unreachable }: { unreachable: boolean }) {
  const { t } = useI18n();
  return <div className="mx-auto flex min-h-screen max-w-[520px] flex-col justify-center gap-4 p-4">
    <div className="flex justify-end"><LangSwitch /></div>
    {unreachable ? <div role="alert" className="card border-red text-red"><strong>{t("net.disconnected")}</strong> <span className="muted">{t("net.disconnectedHelp")}</span></div>
      : <div className="card muted">{t("auth.checking")}</div>}
  </div>;
}

function Authed({ onSignedOut }: { onSignedOut: () => void }) {
  const settings = usePoll<{ ui_lang: Lang }>("/settings", 30000);
  const onLang = useCallback((l: Lang) => { localStorage.setItem("lang", l); void api("/settings", { method: "PUT", body: { ui_lang: l } }).catch(() => undefined); }, []);
  return <I18nProvider initial={settings.data?.ui_lang ?? storedLang()} onChange={onLang}><Shell onSignedOut={onSignedOut} /></I18nProvider>;
}

function Shell({ onSignedOut }: { onSignedOut: () => void }) {
  const { t } = useI18n();
  const [leaving, setLeaving] = useState(false);
  const logout = async () => {
    setLeaving(true);
    try { await api("/auth/logout", { method: "POST", body: {} }); } catch { /* a 401 here means the session is already gone */ }
    onSignedOut();
  };
  const route = useHash();
  const sys = usePoll<System>("/system", 3000);
  const health = usePoll<{ status: string }>("/health", 4000);
  const down = health.disconnected || sys.disconnected;
  const s = sys.data;
  const page = route[0] ?? "home";
  const nav: { id: string; label: string; tag?: string; to?: string }[] = [
    { id: "home", label: t("menu.home"), to: "#/" },
    { id: "tower", label: t("menu.tower"), to: "#/tower" },
    { id: "episodes", label: t("menu.episodes"), to: "#/episodes" },
    { id: "characters", label: t("menu.characters"), to: "#/characters" },
    { id: "review", label: t("menu.review"), to: "#/review" },
    { id: "canon", label: t("menu.canon"), to: "#/canon" },
    { id: "security", label: t("menu.security"), to: "#/security" },
    { id: "providers", label: t("menu.providers"), tag: t("menu.tag.m2"), to: "#/providers" },
    { id: "library", label: t("menu.library"), tag: t("menu.tag.m3") },
  ];
  const chip = (label: string, value: string) => <span className="inner mono whitespace-nowrap text-sm"><span className="muted">{label} </span>{value}</span>;
  return <div className="mx-auto flex min-h-screen max-w-[1440px] flex-col gap-4 p-4">
    <header className="card flex flex-wrap items-center gap-3">
      <div className="mr-auto"><div className="font-display text-xl font-extrabold">{t("app.name")}</div><div className="muted text-sm">{t("app.tagline")}</div></div>
      {chip(t("chip.today"), s ? String(s.episodes_ready_today) : t("app.na"))}
      {chip(t("chip.cpu"), s?.cpu_pct != null ? `${s.cpu_pct.toFixed(0)}%` : t("app.na"))}
      {chip(t("chip.ram"), s?.ram_total_mib ? `${((s.ram_used_mib ?? 0) / 1024).toFixed(1)}/${(s.ram_total_mib / 1024).toFixed(1)} GB` : t("app.na"))}
      {chip(t("chip.vram"), s?.vram_total_mib ? `${((s.vram_used_mib ?? 0) / 1024).toFixed(1)}/${(s.vram_total_mib / 1024).toFixed(1)} GB` : t("app.na"))}
      {chip(t("chip.cloud"), t("chip.cloudNone"))}
      <LangSwitch />
      <div className="flex items-center gap-2"><span aria-hidden className="grid h-11 w-11 place-items-center rounded-full border-[3px] border-outline bg-agent-review font-display font-extrabold text-bg">F</span><div className="text-sm"><div className="font-semibold">Fiat</div><div className="muted">{t("owner.role")}</div></div></div>
      <button className="btn" disabled={leaving} onClick={() => void logout()}>{leaving ? t("auth.loggingOut") : t("auth.logout")}</button>
    </header>
    {down && <div role="alert" className="card border-red text-red"><strong>{t("net.disconnected")}</strong> <span className="muted">{t("net.disconnectedHelp")}</span></div>}
    <div className="grid flex-1 gap-4 md:grid-cols-[240px_1fr]">
      <nav aria-label="menu" className="flex flex-col gap-2">
        {nav.map((n) => n.to
          ? <a key={n.id} href={n.to} aria-current={page === n.id || (n.id === "home" && route.length === 0) ? "page" : undefined} className="card flex min-h-11 items-center justify-between !py-2 font-semibold no-underline aria-[current=page]:border-blue">{n.label}</a>
          : <span key={n.id} aria-disabled className="card flex min-h-11 items-center justify-between !py-2 opacity-60"><span>{n.label}</span><span className="mono rounded border border-line px-1.5 text-xs">{n.tag} · {t("app.notImplemented")}</span></span>)}
        <div className="card text-sm"><div className="font-semibold">{t("gpu.title")}</div>
          {s?.gpu_slot_stage ? <StateLine ok text={t("gpu.busy", { stage: s.gpu_slot_stage })} /> : <StateLine text={t("gpu.idle")} />}</div>
      </nav>
      <main>
        {page === "home" && <Home sys={s} />}
        {page === "episodes" && (route[1] ? <EpisodeDetail id={Number(route[1])} /> : <Episodes />)}
        {page === "characters" && <Characters />}
        {page === "canon" && (route[1] ? <CanonProposalPage id={Number(route[1])} /> : <CanonProposals />)}
        {page === "security" && <Security />}
        {page === "providers" && <Providers />}
        {page === "tower" && <TowerPage />}
        {page === "history" && route[1] && <ArtifactHistory episodeId={Number(route[1])} />}
        {page === "compare" && route[1] && route[2] && <ArtifactCompare a={Number(route[1])} b={Number(route[2])} />}
        {page === "review" &&(route[1] ? <ReviewPage id={Number(route[1])} /> : <ReviewQueue />)}
      </main>
    </div>
  </div>;
}

function StateLine({ text, ok }: { text: string; ok?: boolean }) {
  return <div className={`mono mt-1 flex items-center gap-2 ${ok ? "text-green" : "muted"}`}><span aria-hidden className="h-2.5 w-2.5 rounded-full bg-current" />{text}</div>;
}
