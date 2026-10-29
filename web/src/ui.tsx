import type { ReactNode } from "react";
import { useI18n, type Key } from "./i18n";

const stateColor: Record<string, string> = {
  running: "text-green", completed: "text-blue", qa_passed: "text-green", ready_for_review: "text-amber", approved: "text-green",
  failed: "text-red", qa_failed: "text-red", cancelled: "text-muted", queued: "text-muted", retry_scheduled: "text-amber",
  rejected: "text-red", changes_requested: "text-amber",
};

// StateBadge shows a dot and the state word, so status never relies on colour alone.
export function StateBadge({ state }: { state: string }) {
  const { t } = useI18n();
  const label = `state.${state}` in (useDict()) ? t(`state.${state}` as Key) : state;
  return <span className={`inline-flex items-center gap-2 mono text-sm ${stateColor[state] ?? "text-muted"}`}><span aria-hidden className="h-2.5 w-2.5 rounded-full bg-current" />{label}</span>;
}

import { dictionaries } from "./i18n";
function useDict() { const { lang } = useI18n(); return dictionaries[lang]; }

// LangSwitch is the TH | EN toggle shown on every screen, including the login screen.
export function LangSwitch() {
  const { t, lang, setLang } = useI18n();
  return <div role="group" aria-label={t("lang.label")} className="flex overflow-hidden rounded-xl border-2 border-outline">
    {(["th", "en"] as const).map((l) => <button key={l} className="btn !rounded-none !border-0" aria-pressed={lang === l} style={{ background: lang === l ? "var(--color-blue)" : undefined, color: lang === l ? "#0B1424" : undefined }} onClick={() => setLang(l)}>{l.toUpperCase()}</button>)}
  </div>;
}

export function Card({ title, children, className = "" }: { title?: ReactNode; children: ReactNode; className?: string }) {
  return <section className={`card ${className}`}>{title && <h2 className="mb-3 font-display text-lg font-extrabold">{title}</h2>}{children}</section>;
}

export function Bar({ done, total }: { done: number; total: number }) {
  const pct = total > 0 ? Math.round((100 * done) / total) : 0;
  return <div role="progressbar" aria-valuenow={done} aria-valuemin={0} aria-valuemax={total} className="h-3 w-full overflow-hidden rounded-full border border-line bg-inner">
    <div className="h-full bg-green" style={{ width: `${pct}%` }} />
  </div>;
}

export const fmtBytes = (n: number) => (n >= 1 << 20 ? `${(n / (1 << 20)).toFixed(1)} MB` : `${Math.round(n / 1024)} KB`);
export const fmtTime = (s: string, lang: string) => new Date(s).toLocaleString(lang === "th" ? "th-TH" : "en-GB", { dateStyle: "short", timeStyle: "short" });
export const agentColor: Record<string, string> = {
  script: "var(--color-agent-script)", images: "var(--color-agent-art)", tts: "var(--color-agent-voice)",
  subtitles: "var(--color-agent-voice)", render: "var(--color-agent-render)", qa: "var(--color-agent-qa)",
};
