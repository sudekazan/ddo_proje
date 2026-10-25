import { useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api";
import { useApi, useUser } from "../hooks";
import { Async, Card, ErrorBox, Field } from "../components/ui";
import { date, isoDay, qty, tl } from "../format";

interface Revenue { month: string; charged_kurus: number; collected_kurus: number; refunded_kurus: number }

function Bars({ rows }: { rows: Revenue[] }) {
  const max = Math.max(1, ...rows.map((r) => Math.max(r.charged_kurus, r.collected_kurus)));
  return (
    <div className="bars" role="img" aria-label="Aylık faturalanan ve tahsil edilen">
      {rows.map((r) => (
        <div key={r.month} className="bar-col" title={`${r.month}: faturalanan ${tl(r.charged_kurus)}, tahsilat ${tl(r.collected_kurus)}`}>
          <div className="bar-pair">
            <span className="b1" style={{ height: `${(r.charged_kurus / max) * 100}%` }} />
            <span className="b2" style={{ height: `${(r.collected_kurus / max) * 100}%` }} />
          </div>
          <small>{r.month.slice(2)}</small>
        </div>
      ))}
    </div>
  );
}

export function ReportsPage() {
  const user = useUser()!;
  const yearAgo = isoDay(new Date(Date.now() - 365 * 86400000));
  const today = isoDay(new Date());
  const isAcc = user.role === "accounting";
  const revenue = useApi<{ items: Revenue[] }>(isAcc ? `/api/reports/revenue?from=${yearAgo}&to=${today}` : null);
  const dentists = useApi<{ items: { dentist_id: number; dentist_name: string; completed: number; cancelled: number; no_show: number; charged_kurus: number }[] }>(
    isAcc || user.role === "dentist" ? `/api/reports/dentists?from=${yearAgo}&to=${today}` : null);
  const [day, setDay] = useState(today);
  const units = useApi<{ items: { unit_id: number; unit_name: string; booked_minutes: number; appointments: number; utilization: number }[] }>(
    user.role !== "assistant" ? `/api/reports/units?date=${day}` : null);
  const receivables = useApi<{ total_receivable_kurus: number; overdue_installments_kurus: number; overdue_installments: number; top_debtors: { patient_id: number; patient_name: string; balance_kurus: number }[] }>(
    isAcc ? "/api/reports/receivables" : null);
  const stock = useApi<{ items: { sku: string; name: string; unit: string; consumed_milli: number; returned_milli: number }[] }>(
    isAcc || user.role === "assistant" ? `/api/reports/stock-usage?from=${yearAgo}&to=${today}` : null);

  return (
    <>
      {isAcc && (
        <Card title="Aylık gelir (son 12 ay)" actions={<span className="legend"><i className="sw b1" /> Faturalanan <i className="sw b2" /> Tahsilat</span>}>
          <Async state={revenue} empty={(r) => r.items.length === 0}>{(r) => <Bars rows={r.items} />}</Async>
        </Card>
      )}
      {dentists.data !== undefined || dentists.loading ? (
        <Card title="Hekim performansı (son 12 ay)">
          <Async state={dentists}>
            {(d) => (
              <div className="table-wrap"><table>
                <thead><tr><th>Hekim</th><th className="r">Tamamlanan</th><th className="r">İptal</th><th className="r">Gelmedi</th><th className="r">Faturalanan</th></tr></thead>
                <tbody>{d.items.map((x) => <tr key={x.dentist_id}><td>{x.dentist_name}</td><td className="r">{x.completed}</td><td className="r">{x.cancelled}</td><td className="r">{x.no_show}</td><td className="r">{tl(x.charged_kurus)}</td></tr>)}</tbody>
              </table></div>
            )}
          </Async>
        </Card>
      ) : null}
      {user.role !== "assistant" && (
        <Card title="Ünite doluluğu" actions={<input type="date" value={day} onChange={(e) => setDay(e.target.value)} />}>
          <Async state={units}>
            {(u) => (
              <div className="util">
                {u.items.map((x) => (
                  <div key={x.unit_id} className="util-row">
                    <span>{x.unit_name}</span>
                    <div className="meter"><span style={{ width: `${Math.min(100, x.utilization * 100)}%` }} /></div>
                    <span className="muted">%{Math.round(x.utilization * 100)} · {x.appointments} randevu</span>
                  </div>
                ))}
              </div>
            )}
          </Async>
        </Card>
      )}
      {isAcc && (
        <Card title="Alacaklar">
          <Async state={receivables}>
            {(r) => (
              <>
                <div className="kv"><span>Toplam alacak <b>{tl(r.total_receivable_kurus)}</b></span>
                  <span>Vadesi geçmiş taksit <b>{r.overdue_installments} adet · {tl(r.overdue_installments_kurus)}</b></span></div>
                <table><thead><tr><th>Hasta</th><th className="r">Bakiye</th></tr></thead>
                  <tbody>{r.top_debtors.map((d) => <tr key={d.patient_id}><td><Link to={`/patients/${d.patient_id}`}>{d.patient_name}</Link></td><td className="r debt">{tl(d.balance_kurus)}</td></tr>)}</tbody></table>
              </>
            )}
          </Async>
        </Card>
      )}
      {(isAcc || user.role === "assistant") && (
        <Card title="Sarf malzeme kullanımı (son 12 ay)">
          <Async state={stock}>
            {(s) => (
              <table><thead><tr><th>Malzeme</th><th className="r">Tüketim</th><th className="r">İade</th></tr></thead>
                <tbody>{s.items.map((x) => <tr key={x.sku}><td>{x.name}</td><td className="r">{qty(x.consumed_milli, x.unit)}</td><td className="r">{qty(x.returned_milli, x.unit)}</td></tr>)}</tbody></table>
            )}
          </Async>
        </Card>
      )}
    </>
  );
}

interface Price { id: number; treatment_code: string; treatment_name: string; price_kurus: number; valid_from: string; valid_to: string | null }

export function PricesPage() {
  const user = useUser()!;
  const [day, setDay] = useState(isoDay(new Date()));
  const prices = useApi<{ items: Price[] }>(`/api/prices?date=${day}`);
  const history = useApi<{ items: Price[] }>("/api/prices?history=true");
  const [f, setF] = useState({ treatment_code: "EXAM", price: "", valid_from: isoDay(new Date(Date.now() + 30 * 86400000)) });
  const [error, setError] = useState<Error>();
  async function save() {
    setError(undefined);
    try {
      await api.post("/api/prices", { treatment_code: f.treatment_code, price_kurus: Math.round(Number(f.price.replace(",", ".")) * 100), valid_from: f.valid_from });
      prices.reload();
      history.reload();
    } catch (e) { setError(e as Error); }
  }
  return (
    <>
      <Card title="Fiyat listesi" actions={<input type="date" value={day} onChange={(e) => setDay(e.target.value)} />}>
        <p className="muted">Seçilen tarihte geçerli fiyatlar. Planlar oluşturuldukları tarihin fiyatını korur.</p>
        <Async state={prices}>
          {(p) => (
            <table><thead><tr><th>Tedavi</th><th className="r">Fiyat</th><th>Geçerlilik</th></tr></thead>
              <tbody>{p.items.map((x) => <tr key={x.id}><td>{x.treatment_name}</td><td className="r">{tl(x.price_kurus)}</td><td>{date(x.valid_from)} – {x.valid_to ? date(x.valid_to) : "∞"}</td></tr>)}</tbody></table>
          )}
        </Async>
      </Card>
      {user.role === "accounting" && (
        <Card title="Yeni fiyat dönemi">
          <div className="inline">
            <Field label="Tedavi"><select value={f.treatment_code} onChange={(e) => setF({ ...f, treatment_code: e.target.value })}>
              {history.data && [...new Set(history.data.items.map((x) => x.treatment_code))].map((c) => <option key={c}>{c}</option>)}</select></Field>
            <Field label="Fiyat (₺)"><input value={f.price} onChange={(e) => setF({ ...f, price: e.target.value })} /></Field>
            <Field label="Başlangıç"><input type="date" value={f.valid_from} onChange={(e) => setF({ ...f, valid_from: e.target.value })} /></Field>
            <button className="primary" disabled={!f.price} onClick={save}>Kaydet</button>
          </div>
          {error && <ErrorBox error={error} />}
        </Card>
      )}
    </>
  );
}
