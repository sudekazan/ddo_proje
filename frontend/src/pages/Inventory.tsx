import { useState } from "react";
import { api } from "../api";
import { useApi, useUser } from "../hooks";
import { Async, Card, ErrorBox, Field } from "../components/ui";
import { date, isoDay, qty } from "../format";

interface Supply { id: number; sku: string; name: string; unit: string; min_stock_milli: number; stock_milli: number; expired_milli: number; next_expiry: string | null; below_minimum: boolean }
interface Lot { id: number; lot_no: string; expires_on: string; qty_milli: number; expired: boolean }

export function InventoryPage() {
  const user = useUser()!;
  const supplies = useApi<{ items: Supply[] }>("/api/inventory/supplies", 15000);
  const alerts = useApi<{ items: { id: number; name: string; qty_milli: number; min_stock_milli: number }[] }>("/api/inventory/alerts", 15000);
  const [open, setOpen] = useState<Supply>();
  return (
    <>
      <Async state={alerts}>
        {(a) => a.items.length > 0 ? (
          <div className="error" role="alert">⚠️ Minimum stok altında: {a.items.map((x) => x.name).join(", ")}</div>
        ) : null}
      </Async>
      <Card title="Sarf malzemeleri">
        <Async state={supplies} empty={(s) => s.items.length === 0}>
          {(s) => (
            <div className="table-wrap"><table>
              <thead><tr><th>SKU</th><th>Ad</th><th className="r">Kullanılabilir</th><th className="r">Minimum</th><th className="r">SKT geçmiş</th><th>En yakın SKT</th><th /></tr></thead>
              <tbody>{s.items.map((x) => (
                <tr key={x.id} className={x.below_minimum ? "low" : ""}>
                  <td>{x.sku}</td><td>{x.name}</td><td className="r">{qty(x.stock_milli, x.unit)}</td><td className="r">{qty(x.min_stock_milli, x.unit)}</td>
                  <td className="r muted">{qty(x.expired_milli, x.unit)}</td><td>{x.next_expiry ? date(x.next_expiry) : "—"}</td>
                  <td><button className="link" onClick={() => setOpen(open?.id === x.id ? undefined : x)}>Lotlar</button></td>
                </tr>
              ))}</tbody>
            </table></div>
          )}
        </Async>
      </Card>
      {open && <LotsCard supply={open} canReceive={user.role === "assistant" || user.role === "accounting"} onChange={supplies.reload} />}
    </>
  );
}

function LotsCard({ supply, canReceive, onChange }: { supply: Supply; canReceive: boolean; onChange: () => void }) {
  const lots = useApi<{ items: Lot[] }>(`/api/inventory/supplies/${supply.id}/lots`);
  const [f, setF] = useState({ lot_no: "", expires_on: isoDay(new Date(Date.now() + 365 * 86400000)), qty: "" });
  const [error, setError] = useState<Error>();
  async function receive() {
    setError(undefined);
    try {
      await api.post(`/api/inventory/supplies/${supply.id}/lots`, { lot_no: f.lot_no, expires_on: f.expires_on, qty_milli: Math.round(Number(f.qty.replace(",", ".")) * 1000) });
      setF({ ...f, lot_no: "", qty: "" });
      lots.reload();
      onChange();
    } catch (e) { setError(e as Error); }
  }
  return (
    <Card title={`${supply.name} — lotlar (FEFO sırası)`}>
      <Async state={lots} empty={(l) => l.items.length === 0}>
        {(l) => (
          <table>
            <thead><tr><th>Lot</th><th>SKT</th><th className="r">Miktar</th></tr></thead>
            <tbody>{l.items.map((x) => (
              <tr key={x.id} className={x.expired ? "muted strike" : ""}><td>{x.lot_no}</td><td>{date(x.expires_on)}{x.expired ? " (geçmiş)" : ""}</td><td className="r">{qty(x.qty_milli, supply.unit)}</td></tr>
            ))}</tbody>
          </table>
        )}
      </Async>
      {canReceive && (
        <div className="inline">
          <Field label="Lot no"><input value={f.lot_no} onChange={(e) => setF({ ...f, lot_no: e.target.value })} /></Field>
          <Field label="SKT"><input type="date" value={f.expires_on} onChange={(e) => setF({ ...f, expires_on: e.target.value })} /></Field>
          <Field label={`Miktar (${supply.unit})`}><input inputMode="decimal" value={f.qty} onChange={(e) => setF({ ...f, qty: e.target.value })} /></Field>
          <button className="primary" disabled={!f.lot_no || !f.qty} onClick={receive}>Stok girişi</button>
        </div>
      )}
      {error && <ErrorBox error={error} />}
    </Card>
  );
}
