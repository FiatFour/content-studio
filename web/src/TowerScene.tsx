import { useI18n, type Key } from "./i18n";
import {
  agentColour, carCentreY, carFloor, CAR_H, floorBox, FLOOR_CODES, FLOOR_H, isKnownState, VIEW, visualOf,
  type Floor, type TowerState,
} from "./tower";

// The scene is drawn by hand: a thick-outline cutaway building, original shapes only. It is static (no animation,
// T3 #71) and every lamp, mark and car position comes from the GET /api/tower payload. Side panels (T4 #72) plug in
// next to this component: it only needs the tower payload, a selected floor code and a select callback.

const OUT = "#1B1B1B";
const PAPER = "#F9FBFE";
const INK = "#1B1B1B";

// LampGlyph draws one status mark centred on (0,0), r = 10. Shape differs per state: tick, dot, ring, cross, pause, dash.
export function LampGlyph({ state }: { state: string }) {
  const v = visualOf(state);
  const common = { stroke: OUT, strokeWidth: 2.5 };
  switch (v.glyph) {
    case "check": return <g><circle r="10" fill={v.color} {...common} /><path d="M-5 0l3.5 4 6.5-8" fill="none" stroke={OUT} strokeWidth="3" strokeLinecap="round" strokeLinejoin="round" /></g>;
    case "dot": return <g><circle r="10" fill={v.color} {...common} /><circle r="3.5" fill={OUT} /></g>;
    case "cross": return <g><circle r="10" fill={v.color} {...common} /><path d="M-4.5-4.5l9 9M4.5-4.5l-9 9" stroke={OUT} strokeWidth="3" strokeLinecap="round" /></g>;
    case "pause": return <g><circle r="10" fill={v.color} {...common} /><path d="M-3.5-5v10M3.5-5v10" stroke={OUT} strokeWidth="3" strokeLinecap="round" /></g>;
    case "dash": return <g><circle r="10" fill="none" stroke={OUT} strokeWidth="2.5" strokeDasharray="4 3" /><path d="M-5 0h10" stroke={OUT} strokeWidth="3" strokeLinecap="round" /></g>;
    default: return <circle r="9" fill="#C8D2E2" {...common} />;
  }
}

function StatusDot({ state }: { state: string }) {
  return <svg aria-hidden width="18" height="18" viewBox="-12 -12 24 24" className="shrink-0"><LampGlyph state={state} /></svg>;
}

// Props in each room are decoration only. They are the same whatever the state; state shows in lamps, outline and text.
function Props({ agent, y }: { agent: string; y: number }) {
  const base = y + 82; // floor line inside the room
  const sk = { stroke: OUT, strokeWidth: 3.5, strokeLinejoin: "round" as const, strokeLinecap: "round" as const };
  switch (agent) {
    case "guardrails": return <g {...sk}><circle cx="620" cy={y + 46} r="30" fill="#B7C7B0" /><circle cx="620" cy={y + 46} r="14" fill="#D9E4D5" /><path d={`M620 ${y + 32}v28M606 ${y + 46}h28`} fill="none" /><rect x="530" y={base - 22} width="40" height="22" rx="4" fill="#B7C7B0" /></g>;
    case "orchestrator": return <g {...sk}><rect x="520" y={base - 34} width="130" height="34" rx="5" fill="#E8D9B8" /><rect x="540" y={base - 62} width="60" height="28" rx="4" fill="#CFE0F2" /><circle cx="622" cy={base - 48} r="9" fill="#F5B942" /><path d={`M690 ${base}v-40h20v40`} fill="#8FB3D9" /></g>;
    case "script": return <g {...sk}><rect x="520" y={base - 30} width="140" height="30" rx="5" fill="#E8D9B8" /><rect x="540" y={base - 46} width="48" height="16" fill="#fff" /><rect x="550" y={base - 58} width="48" height="14" fill="#FFF3F8" /><path d={`M690 ${base - 6}l26-34 8 6-26 34z`} fill="#F28DB2" /></g>;
    case "art": return <g {...sk}><path d={`M560 ${base}l22-62 22 62M582 ${base - 62}v62`} fill="none" /><rect x="548" y={base - 66} width="68" height="44" rx="3" fill="#fff" /><circle cx="570" cy={base - 48} r="7" fill="#59C9B9" /><path d={`M584 ${base - 30}l12-14 14 14z`} fill="#F2C94C" /><rect x="660" y={base - 24} width="40" height="24" rx="4" fill="#CFEFEA" /></g>;
    case "voice": return <g {...sk}><path d={`M600 ${base}v-40M582 ${base}h36`} fill="none" /><rect x="588" y={base - 74} width="24" height="38" rx="12" fill="#D8CCF8" /><path d={`M578 ${base - 54}a22 22 0 0 0 44 0`} fill="none" /><rect x="650" y={base - 44} width="54" height="44" rx="6" fill="#D8CCF8" /><circle cx="677" cy={base - 22} r="12" fill="#fff" /></g>;
    case "render": return <g {...sk}><rect x="530" y={base - 48} width="90" height="48" rx="5" fill="#F7E9AE" /><circle cx="560" cy={base - 24} r="12" fill="#fff" /><circle cx="560" cy={base - 24} r="4" fill={OUT} /><path d={`M590 ${base - 12}h20`} fill="none" /><rect x="650" y={base - 34} width="56" height="34" rx="4" fill="#fff" /><path d={`M668 ${base - 28}l18 11-18 11z`} fill="#F2C94C" /></g>;
    case "qa": return <g {...sk}><circle cx="590" cy={base - 42} r="22" fill="#FFF1E4" /><path d={`M606 ${base - 26}l26 26`} fill="none" strokeWidth="7" /><rect x="660" y={base - 40} width="44" height="40" rx="4" fill="#fff" /><path d={`M670 ${base - 22}l8 8 16-18`} fill="none" stroke="#F29B5C" strokeWidth="5" /></g>;
    case "review": return <g {...sk}><path d={`M580 ${base}v-26M650 ${base}v-26`} fill="none" /><rect x="574" y={base - 34} width="84" height="12" rx="4" fill="#E9573F" /><path d={`M574 ${base - 34}v-40q0-8 8-8h12q8 0 8 8v30`} fill="#E9573F" /><path d={`M690 ${base}v-30h18v30`} fill="#B8C4D8" /></g>;
    default: return null;
  }
}

function Hologram() {
  const sk = { stroke: OUT, strokeWidth: 3.5, strokeLinejoin: "round" as const };
  return <g aria-hidden {...sk}><rect x="320" y="128" width="90" height="12" rx="4" fill="#B8C4D8" /><path d="M365 128v-14" fill="none" /><path d="M365 54l30 28-30 28-30-28z" fill="#E8D27A" fillOpacity="0.55" strokeDasharray="6 4" /></g>;
}

function DronePad({ label }: { label: string }) {
  const sk = { stroke: OUT, strokeWidth: 3.5, strokeLinejoin: "round" as const, strokeLinecap: "round" as const };
  return <g aria-hidden {...sk}>
    <rect x="500" y="128" width="160" height="12" rx="4" fill="#B8C4D8" />
    <g><ellipse cx="600" cy="92" rx="26" ry="12" fill="#E9EEF7" /><path d="M570 78h-22M630 78h22M548 78v-6M652 78v-6" fill="none" /><rect x="586" y="102" width="28" height="20" rx="3" fill="#fff" /><path d="M594 110h12M594 116h8" fill="none" strokeWidth="2.5" /></g>
    <text x="600" y="60" textAnchor="middle" fontSize="13" fontWeight="700" fill="#EAF0FA" stroke="none" fontFamily="var(--font-sans)">{label}</text>
  </g>;
}

type SceneProps = { tower: TowerState; frozen?: boolean; selected?: string | null; onSelect?: (code: string) => void };

export function Tower({ tower, frozen = false, selected = null, onSelect }: SceneProps) {
  const { t } = useI18n();
  const byCode = new Map(tower.floors.map((f) => [f.code, f]));
  const car = carFloor(tower);
  const carCode = car ?? "G";
  const cy = carCentreY(carCode);
  const pct = (n: number, d: number) => `${(100 * n) / d}%`;
  const stateWord = (s: string) => (isKnownState(s) ? t(`tower.state.${s}` as Key) : s);

  return <div className="relative" style={{ aspectRatio: `${VIEW.w} / ${VIEW.h}`, minWidth: 900, filter: frozen ? "grayscale(.7) opacity(.75)" : undefined }} data-frozen={frozen || undefined}>
    <svg role="img" aria-labelledby="tower-title tower-desc" viewBox={`0 0 ${VIEW.w} ${VIEW.h}`} className="absolute inset-0 h-full w-full" fontFamily="var(--font-sans)">
      <title id="tower-title">{t("tower.svgTitle")}</title>
      <desc id="tower-desc">{t("tower.svgDesc")}</desc>
      <defs>
        <pattern id="tower-hatch" width="10" height="10" patternUnits="userSpaceOnUse" patternTransform="rotate(45)"><path d="M0 0v10" stroke={OUT} strokeWidth="2" opacity=".35" /></pattern>
      </defs>
      <rect width={VIEW.w} height={VIEW.h} fill="#1E3A63" />
      <rect y="872" width={VIEW.w} height="18" fill="#3B2F2A" stroke={OUT} strokeWidth="4" />

      {/* building: one slab per floor */}
      {FLOOR_CODES.slice(1).map((code) => {
        const f = byCode.get(code);
        const b = floorBox(code);
        const agent = f?.agent ?? "";
        const v = visualOf(f?.state ?? "idle");
        const ni = f?.state === "not_implemented";
        return <g key={code} aria-hidden>
          <rect x="30" y={b.y} width="700" height={b.h} fill={`color-mix(in srgb, ${agentColour[agent] ?? "#8FB3D9"} 38%, #F4F7FC)`} stroke={OUT} strokeWidth="5" />
          <Props agent={agent} y={b.y} />
          {ni && <rect x="500" y={b.y + 5} width="226" height={b.h - 10} fill="url(#tower-hatch)" />}
          <rect x="36" y={b.y + 6} width="688" height={b.h - 12} fill="none" stroke={v.color} strokeWidth={f?.state && f.state !== "idle" && f.state !== "not_implemented" ? 5 : 0} strokeDasharray={v.dashed ? "10 6" : undefined} rx="4" />
        </g>;
      })}
      {/* below ground is darker */}
      <rect x="30" y={floorBox("B1").y} width="700" height="90" fill="#000" opacity=".22" aria-hidden />

      {/* roof */}
      <g aria-hidden>
        <rect x="24" y="138" width="712" height="14" rx="4" fill="#5D6C86" stroke={OUT} strokeWidth="5" />
        <g transform="translate(315 0)"><Hologram /></g>
        <g transform="translate(-30 0)"><DronePad label={t("tower.padLabel")} /></g>
      </g>

      {/* label plates behind the buttons */}
      {FLOOR_CODES.map((code) => {
        const b = floorBox(code);
        const y = code === "Roof" ? 22 : b.y + 8;
        const h = code === "Roof" ? 100 : b.h - 16;
        return <rect key={code} aria-hidden x="44" y={y} width="442" height={h} rx="12" fill={PAPER} fillOpacity=".95" stroke={OUT} strokeWidth="3.5" />;
      })}

      {/* pipeline elevator */}
      <g aria-hidden>
        <rect x="748" y="2" width="204" height="44" rx="6" fill="#8A97AB" stroke={OUT} strokeWidth="5" />
        <circle cx="800" cy="24" r="12" fill="#C8D2E2" stroke={OUT} strokeWidth="3.5" /><path d="M830 24h96" stroke={OUT} strokeWidth="4" strokeLinecap="round" />
        <rect x="770" y="46" width="160" height="826" fill="#9CCBF2" fillOpacity=".28" stroke={OUT} strokeWidth="5" />
        {[0, 1].map((i) => <rect key={i} x={i ? 920 : 762} y="46" width="18" height="826" fill="#8A97AB" stroke={OUT} strokeWidth="4" />)}
        {Array.from({ length: 8 }, (_, i) => <path key={i} d={`M778 ${50 + i * 103}l142 103M920 ${50 + i * 103}l-142 103`} stroke={OUT} strokeWidth="2" opacity=".28" />)}
        <path d={`M850 46V${cy - CAR_H / 2}`} stroke={OUT} strokeWidth="4" />
        <rect x="790" y={cy - CAR_H / 2} width="120" height={CAR_H} rx="8" fill="#F2F5FA" stroke={OUT} strokeWidth="5" />
        <rect x="802" y={cy - CAR_H / 2 + 9} width="96" height={CAR_H - 28} rx="4" fill="#9CCBF2" stroke={OUT} strokeWidth="3" />
        <path d={`M850 ${cy - CAR_H / 2 + 9}v${CAR_H - 28}`} stroke={OUT} strokeWidth="3" />
        {car && <g transform={`translate(850 ${cy + CAR_H / 2 - 9})`}><LampGlyph state={byCode.get(car)?.state ?? "idle"} /></g>}
      </g>

      {/* landing signs: floor code and status lamp */}
      {FLOOR_CODES.map((code) => {
        const f = byCode.get(code);
        const c = code === "Roof" ? 75 : floorBox(code).y + FLOOR_H / 2;
        return <g key={code} aria-hidden transform={`translate(0 ${c})`}>
          <rect x="942" y="-18" width="56" height="36" rx="8" fill={PAPER} stroke={OUT} strokeWidth="3.5" />
          <g transform="translate(958 0) scale(.8)"><LampGlyph state={f?.state ?? "idle"} /></g>
          <text x="984" y="5" textAnchor="middle" fontSize="13" fontWeight="800" fill={INK} fontFamily="var(--font-mono)">{code === "Roof" ? "R" : code}</text>
        </g>;
      })}
    </svg>

    {/* floor labels: real buttons laid over the plates */}
    {FLOOR_CODES.map((code) => {
      const f: Floor | undefined = byCode.get(code);
      if (!f) return null;
      const b = floorBox(code);
      const y = code === "Roof" ? 22 : b.y + 8;
      const h = code === "Roof" ? 100 : b.h - 16;
      const name = t(`tower.agent.${f.agent}` as Key);
      const isSel = selected === code;
      return <button key={code} type="button" aria-pressed={isSel} onClick={() => onSelect?.(code)} data-floor={code} data-state={f.state}
        className="absolute flex min-h-11 flex-col justify-center gap-0.5 overflow-hidden rounded-xl px-3 text-left text-[#1B1B1B] focus-visible:outline-[3px] focus-visible:outline-offset-2 focus-visible:outline-blue"
        style={{ left: pct(44, VIEW.w), top: pct(y, VIEW.h), width: pct(442, VIEW.w), height: pct(h, VIEW.h), boxShadow: isSel ? "0 0 0 4px var(--color-blue)" : undefined, background: "transparent", cursor: "pointer" }}>
        <span className="flex items-baseline gap-2 leading-tight">
          <span className="mono rounded border-2 border-[#1B1B1B] px-1.5 text-sm font-bold">{code === "Roof" ? t("tower.roof") : code}</span>
          <span className="font-display text-base font-extrabold">{name}</span>
        </span>
        <span className="text-xs leading-tight">{t(`tower.desc.${f.agent}` as Key)}</span>
        <span className="mono flex items-center gap-1.5 text-xs font-bold leading-tight"><StatusDot state={f.state} />{stateWord(f.state)}</span>
      </button>;
    })}
  </div>;
}
