import { createContext, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import th from "./th.json";
import en from "./en.json";

export type Lang = "th" | "en";
export type Key = keyof typeof th;

export const dictionaries: Record<Lang, Record<string, string>> = { th, en };

export function translate(lang: Lang, key: string, vars?: Record<string, string | number>): string {
  let s = dictionaries[lang][key] ?? key;
  for (const [k, v] of Object.entries(vars ?? {})) s = s.replaceAll(`{${k}}`, String(v));
  return s;
}

type Ctx = { lang: Lang; setLang: (l: Lang) => void; t: (key: Key, vars?: Record<string, string | number>) => string };
const I18n = createContext<Ctx | null>(null);

export function I18nProvider({ initial, onChange, children }: { initial: Lang; onChange: (l: Lang) => void; children: ReactNode }) {
  const [lang, setLangState] = useState<Lang>(initial);
  useEffect(() => setLangState(initial), [initial]);
  useEffect(() => { document.documentElement.lang = lang; }, [lang]);
  const value = useMemo<Ctx>(() => ({
    lang,
    setLang: (l) => { setLangState(l); onChange(l); },
    t: (key, vars) => translate(lang, key, vars),
  }), [lang, onChange]);
  return <I18n.Provider value={value}>{children}</I18n.Provider>;
}

export function useI18n(): Ctx {
  const c = useContext(I18n);
  if (!c) throw new Error("useI18n outside I18nProvider");
  return c;
}
