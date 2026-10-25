import { toothStateLabel } from "../format";

export interface ToothState { tooth: number; state: string; updated_at?: string | null }

const upperPermanent = [18, 17, 16, 15, 14, 13, 12, 11, 21, 22, 23, 24, 25, 26, 27, 28];
const lowerPermanent = [48, 47, 46, 45, 44, 43, 42, 41, 31, 32, 33, 34, 35, 36, 37, 38];
const upperPrimary = [55, 54, 53, 52, 51, 61, 62, 63, 64, 65];
const lowerPrimary = [85, 84, 83, 82, 81, 71, 72, 73, 74, 75];

// ROW_GAP leaves room for the tooth numbers of both jaws between the rows.
const W = 36, H = 44, GAP = 4, ROW_GAP = 44;

// Interactive FDI chart: click a tooth to see its timeline.
export function ToothChart({ teeth, selected, onSelect, primary }: {
  teeth: ToothState[]; selected?: number; onSelect: (t: number) => void; primary?: boolean;
}) {
  const state = new Map(teeth.map((t) => [t.tooth, t.state]));
  const rows = primary ? [upperPrimary, lowerPrimary] : [upperPermanent, lowerPermanent];
  const cols = rows[0].length;
  const width = cols * (W + GAP) + 16;
  return (
    <div className="chart-wrap">
    <svg className="chart" viewBox={`0 0 ${width} ${2 * H + ROW_GAP + 8}`} role="img" aria-label="Diş haritası">
      <line x1={width / 2} y1={0} x2={width / 2} y2={2 * H + ROW_GAP + 8} className="midline" />
      <line x1={0} y1={H + ROW_GAP / 2 + 4} x2={width} y2={H + ROW_GAP / 2 + 4} className="midline" />
      {rows.map((row, ri) =>
        row.map((t, i) => {
          const x = 8 + i * (W + GAP) + (i >= cols / 2 ? 4 : 0) - (i >= cols / 2 ? 0 : 4);
          const y = ri === 0 ? 4 : H + ROW_GAP + 4;
          const st = state.get(t) ?? "healthy";
          return (
            <g key={t} className={`tooth s-${st} ${selected === t ? "sel" : ""}`} onClick={() => onSelect(t)}
              tabIndex={0} role="button" aria-label={`${t} ${toothStateLabel[st]}`}
              onKeyDown={(e) => (e.key === "Enter" || e.key === " ") && onSelect(t)}>
              <title>{`${t} — ${toothStateLabel[st]}`}</title>
              <rect x={x} y={y} width={W} height={H} rx={10} />
              {st === "extracted" && <path d={`M${x + 8} ${y + 8} L${x + W - 8} ${y + H - 8} M${x + W - 8} ${y + 8} L${x + 8} ${y + H - 8}`} className="cross" />}
              <text x={x + W / 2} y={ri === 0 ? y + H + 13 : y - 6} textAnchor="middle" className="num">{t}</text>
            </g>
          );
        }),
      )}
    </svg>
    </div>
  );
}

export function ToothLegend() {
  return (
    <div className="legend">
      {Object.entries(toothStateLabel).map(([k, v]) => <span key={k}><i className={`sw s-${k}`} /> {v}</span>)}
    </div>
  );
}
