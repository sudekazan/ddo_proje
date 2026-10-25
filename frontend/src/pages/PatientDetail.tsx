import { useState } from "react";
import { useParams } from "react-router-dom";
import { api } from "../api";
import { useApi, useUser } from "../hooks";
import { Async, Badge, Card, Empty, ErrorBox, Field, Loading } from "../components/ui";
import { ToothChart, ToothLegend, ToothState } from "../components/ToothChart";
import { BookingForm } from "../components/BookingForm";
import { date, dateTime, tl, toothStateLabel } from "../format";
import { Appointment, Page, Patient, Plan, TreatmentType } from "../types";
import { AppointmentActions } from "./Appointments";

type Tab = "chart" | "appointments" | "plans" | "account" | "sms";

export function PatientDetail({ id: fixedId }: { id?: number }) {
  const params = useParams();
  const id = fixedId ?? Number(params.id);
  const user = useUser()!;
  const patient = useApi<Patient>(`/api/patients/${id}`);
  const [tab, setTab] = useState<Tab>("chart");
  const canFinance = ["accounting", "reception", "patient"].includes(user.role);
  const tabs: [Tab, string, boolean][] = [
    ["chart", "Diş haritası", user.role !== "accounting"],
    ["appointments", "Randevular", true],
    ["plans", "Tedavi planları", true],
    ["account", "Cari hesap", canFinance],
    ["sms", "SMS bildirimleri", user.role === "patient" || user.role === "reception"],
  ];
  return (
    <Async state={patient}>
      {(p) => (
        <>
          <Card title={`${p.first_name} ${p.last_name}`}>
            <div className="kv">
              <span>Hasta no <b>{p.id}</b></span><span>TC <b>{p.national_id}</b></span><span>Doğum <b>{date(p.birth_date)}</b></span>
              <span>Telefon <b>{p.phone}</b></span><span>Kurum <b>{p.institution ? `${p.institution} (%${p.discount_bp / 100})` : "—"}</b></span>
              <span>Bakiye <b className={p.balance_kurus > 0 ? "debt" : ""}>{tl(p.balance_kurus)}</b></span>
            </div>
          </Card>
          <nav className="tabs" role="tablist">
            {tabs.filter((t) => t[2]).map(([k, label]) => (
              <button key={k} role="tab" aria-selected={tab === k} className={tab === k ? "on" : ""} onClick={() => setTab(k)}>{label}</button>
            ))}
          </nav>
          {tab === "chart" && <ChartTab patientId={id} />}
          {tab === "appointments" && <AppointmentsTab patientId={id} />}
          {tab === "plans" && <PlansTab patientId={id} onChange={patient.reload} />}
          {tab === "account" && <AccountTab patientId={id} />}
          {tab === "sms" && <SmsTab patientId={id} />}
        </>
      )}
    </Async>
  );
}

interface ToothEvent { id: number; tooth: number; treatment_name: string; state_before: string; state_after: string; performed_at: string; dentist_name: string | null; note: string; reverted_at: string | null }

function ChartTab({ patientId }: { patientId: number }) {
  const user = useUser()!;
  const chart = useApi<{ permanent: ToothState[]; primary: ToothState[] }>(`/api/patients/${patientId}/teeth`);
  const [tooth, setTooth] = useState<number>();
  const [primary, setPrimary] = useState(false);
  const events = useApi<Page<ToothEvent>>(tooth ? `/api/patients/${patientId}/teeth/${tooth}/events` : `/api/patients/${patientId}/tooth-events?limit=20`);
  const [obs, setObs] = useState("extracted");
  const [error, setError] = useState<Error>();

  async function observe() {
    setError(undefined);
    try {
      await api.post(`/api/patients/${patientId}/teeth/${tooth}/observations`, { state: obs, note: "Muayene bulgusu" });
      chart.reload();
      events.reload();
    } catch (e) { setError(e as Error); }
  }
  return (
    <div className="split">
      <Card title="Diş haritası (FDI)" actions={
        <label className="toggle"><input type="checkbox" checked={primary} onChange={(e) => setPrimary(e.target.checked)} /> Süt dişleri</label>}>
        <Async state={chart}>
          {(c) => <ToothChart teeth={primary ? c.primary : c.permanent} primary={primary} selected={tooth} onSelect={setTooth} />}
        </Async>
        <ToothLegend />
        {tooth && user.role === "dentist" && (
          <div className="inline">
            <Field label={`${tooth} için bulgu kaydet`}>
              <select value={obs} onChange={(e) => setObs(e.target.value)}>
                {Object.entries(toothStateLabel).map(([k, v]) => <option key={k} value={k}>{v}</option>)}
              </select>
            </Field>
            <button onClick={observe}>Kaydet</button>
          </div>
        )}
        {error && <ErrorBox error={error} />}
      </Card>
      <Card title={tooth ? `${tooth} numaralı diş — zaman çizelgesi` : "Son işlemler"} actions={tooth && <button className="link" onClick={() => setTooth(undefined)}>Tümü</button>}>
        <Async state={events} empty={(e) => e.items.length === 0}>
          {(e) => (
            <ol className="timeline">
              {e.items.map((ev) => (
                <li key={ev.id} className={ev.reverted_at ? "reverted" : ""}>
                  <div className="when">{dateTime(ev.performed_at)}</div>
                  <div><b>{ev.tooth}</b> · {ev.treatment_name} <span className="muted">{toothStateLabel[ev.state_before]} → {toothStateLabel[ev.state_after]}</span></div>
                  <div className="muted">{ev.dentist_name ?? ""} {ev.note}{ev.reverted_at ? " · geri alındı" : ""}</div>
                </li>
              ))}
            </ol>
          )}
        </Async>
      </Card>
    </div>
  );
}

function AppointmentsTab({ patientId }: { patientId: number }) {
  const user = useUser()!;
  const list = useApi<Page<Appointment>>(`/api/appointments?patient_id=${patientId}&order=desc&limit=50`);
  const [booking, setBooking] = useState(false);
  const canBook = user.role === "reception" || user.role === "patient";
  return (
    <Card title="Randevular" actions={canBook && <button className="primary" onClick={() => setBooking(!booking)}>+ Randevu al</button>}>
      {booking && <BookingForm patientId={patientId} onBooked={() => { setBooking(false); list.reload(); }} />}
      <Async state={list} empty={(p) => p.items.length === 0}>
        {(p) => <AppointmentTable items={p.items} onChange={list.reload} />}
      </Async>
    </Card>
  );
}

export function AppointmentTable({ items, onChange, showPatient }: { items: Appointment[]; onChange: () => void; showPatient?: boolean }) {
  return (
    <div className="table-wrap">
      <table>
        <thead><tr><th>Tarih</th>{showPatient && <th>Hasta</th>}<th>Tedavi</th><th>Diş</th><th>Hekim</th><th>Ünite / Asistan</th><th>Durum</th><th /></tr></thead>
        <tbody>
          {items.map((a) => (
            <tr key={a.id}>
              <td>{dateTime(a.start)}</td>
              {showPatient && <td>{a.patient_name} <span className="muted">#{a.patient_id}</span></td>}
              <td>{a.treatment_name}{a.session_no ? <span className="muted"> · seans {a.session_no}</span> : null}</td>
              <td>{a.tooth ?? "—"}</td>
              <td>{a.dentist_name}</td>
              <td>{a.unit_name} / {a.assistant_name}</td>
              <td><Badge status={a.status} /></td>
              <td><AppointmentActions a={a} onChange={onChange} /></td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function PlansTab({ patientId, onChange }: { patientId: number; onChange: () => void }) {
  const user = useUser()!;
  const plans = useApi<{ items: Plan[] }>(`/api/patients/${patientId}/plans`);
  const [creating, setCreating] = useState(false);
  return (
    <>
      {user.role === "dentist" && (
        <div className="bar"><button className="primary" onClick={() => setCreating(!creating)}>+ Yeni tedavi planı</button></div>
      )}
      {creating && <PlanEditor patientId={patientId} onDone={() => { setCreating(false); plans.reload(); }} />}
      <Async state={plans} empty={(p) => p.items.length === 0}>
        {(p) => <>{p.items.map((pl) => <PlanCard key={pl.id} plan={pl} onChange={() => { plans.reload(); onChange(); }} />)}</>}
      </Async>
    </>
  );
}

interface Draft { treatment_code: string; tooth: string; depends_on: string }

export function PlanEditor({ patientId, planId, onDone }: { patientId: number; planId?: number; onDone: () => void }) {
  const types = useApi<{ items: TreatmentType[] }>("/api/treatment-types");
  const [items, setItems] = useState<Draft[]>([{ treatment_code: "ROOT_CANAL", tooth: "36", depends_on: "" }, { treatment_code: "CROWN", tooth: "36", depends_on: "" }]);
  const [note, setNote] = useState("");
  const [error, setError] = useState<Error>();
  const update = (i: number, k: keyof Draft, v: string) => setItems(items.map((it, j) => (j === i ? { ...it, [k]: v } : it)));

  async function save() {
    setError(undefined);
    const body = {
      patient_id: patientId, note,
      items: items.map((it) => ({
        treatment_code: it.treatment_code,
        tooth: it.tooth ? Number(it.tooth) : null,
        depends_on: it.depends_on ? Number(it.depends_on) : null,
      })),
    };
    try {
      if (planId) await api.post(`/api/plans/${planId}/revisions`, body);
      else await api.post("/api/plans", body);
      onDone();
    } catch (e) { setError(e as Error); }
  }
  if (types.loading) return <Loading />;
  return (
    <Card title={planId ? "Planı revize et (yeni sürüm)" : "Yeni tedavi planı"}>
      <p className="muted">Aynı dişte kanal → kuron, çekim → implant sıralaması otomatik kurulur. Bağımlılık için kalem numarası girin.</p>
      {items.map((it, i) => {
        const tt = types.data?.items.find((t) => t.code === it.treatment_code);
        return (
          <div className="grid4 item-row" key={i}>
            <Field label={`${i + 1}. kalem`}>
              <select value={it.treatment_code} onChange={(e) => update(i, "treatment_code", e.target.value)}>
                {types.data?.items.map((t) => <option key={t.code} value={t.code}>{t.name}{t.sessions > 1 ? ` (${t.sessions} seans)` : ""}</option>)}
              </select>
            </Field>
            <Field label="Diş (FDI)"><input value={it.tooth} disabled={!tt?.requires_tooth} onChange={(e) => update(i, "tooth", e.target.value)} /></Field>
            <Field label="Bağlı olduğu kalem"><input value={it.depends_on} onChange={(e) => update(i, "depends_on", e.target.value)} placeholder="—" /></Field>
            <button className="link danger" onClick={() => setItems(items.filter((_, j) => j !== i))}>Kaldır</button>
          </div>
        );
      })}
      <div className="bar">
        <button onClick={() => setItems([...items, { treatment_code: "FILLING", tooth: "", depends_on: "" }])}>+ Kalem ekle</button>
        <input placeholder="Not" value={note} onChange={(e) => setNote(e.target.value)} />
        <button className="primary" onClick={save} disabled={items.length === 0}>Kaydet</button>
      </div>
      {error && <ErrorBox error={error} />}
    </Card>
  );
}

function PlanCard({ plan, onChange }: { plan: Plan; onChange: () => void }) {
  const user = useUser()!;
  const [error, setError] = useState<Error>();
  const [bookItem, setBookItem] = useState<number>();
  const [revising, setRevising] = useState(false);
  const [inst, setInst] = useState({ down: "1000", count: "3", first: "" });
  const v = plan.versions.find((x) => x.version === plan.current_version)!;
  const installments = useApi<{ items: { id: number; seq: number; due_date: string; amount_kurus: number; paid_kurus: number; status: string }[] }>(
    user.role !== "dentist" && user.role !== "assistant" ? `/api/plans/${plan.id}/installments` : null);

  async function act(fn: () => Promise<unknown>) {
    setError(undefined);
    try { await fn(); onChange(); installments.reload(); } catch (e) { setError(e as Error); }
  }
  const open = plan.status !== "cancelled" && plan.status !== "completed";
  return (
    <Card title={<>Plan #{plan.id} · sürüm {plan.current_version} <Badge status={plan.status} /></>} actions={
      <>
        {plan.status === "draft" && (user.role === "patient" || user.role === "reception") && (
          <button className="primary" onClick={() => act(() => api.post(`/api/plans/${plan.id}/approve`, { version: plan.current_version }))}>
            {user.role === "patient" ? "Planı onayla" : "İmzalı onam kaydet"}
          </button>
        )}
        {user.role === "dentist" && (plan.status === "draft" || plan.status === "approved") && <button onClick={() => setRevising(!revising)}>Revize et</button>}
        {open && (user.role === "dentist" || user.role === "accounting") && (
          <button className="danger" onClick={() => confirm("Plan iptal edilsin mi? Fazla ödeme iade edilir.") &&
            act(() => api.post(`/api/plans/${plan.id}/cancel`, { reason: "Arayüzden iptal" }))}>İptal et</button>
        )}
      </>}>
      <div className="kv">
        <span>Hekim <b>{plan.dentist_name}</b></span><span>Fiyat tarihi <b>{date(v.price_date)}</b></span>
        <span>Brüt <b>{tl(v.gross_kurus)}</b></span><span>İndirim <b>{tl(v.discount_kurus)}</b> (%{v.discount_bp / 100})</span>
        <span>Net <b>{tl(v.net_kurus)}</b></span>
        <span>Onay <b>{v.approved_at ? `${dateTime(v.approved_at)} (${v.approval_method === "patient_portal" ? "hasta portalı" : "imzalı form"})` : "bekliyor"}</b></span>
      </div>
      {revising && <PlanEditor patientId={plan.patient_id} planId={plan.id} onDone={() => { setRevising(false); onChange(); }} />}
      <div className="table-wrap">
        <table>
          <thead><tr><th>#</th><th>Tedavi</th><th>Diş</th><th>Bağımlılık</th><th>Seans</th><th className="r">Net</th><th className="r">Faturalanan</th><th>Durum</th><th /></tr></thead>
          <tbody>
            {plan.items.map((it) => (
              <tr key={it.id}>
                <td>{it.seq}</td><td>{it.treatment_name}</td><td>{it.tooth ?? "—"}</td>
                <td>{it.depends_on ? `#${plan.items.find((x) => x.id === it.depends_on)?.seq}` : "—"}</td>
                <td>{it.sessions_done}/{it.sessions_total}</td>
                <td className="r">{tl(it.net_kurus)}</td><td className="r">{tl(it.billed_kurus)}</td>
                <td><Badge status={it.status} /></td>
                <td>{user.role === "reception" && ["approved", "in_progress"].includes(plan.status) && ["pending", "in_progress"].includes(it.status) &&
                  <button className="link" onClick={() => setBookItem(bookItem === it.id ? undefined : it.id)}>Seans planla</button>}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {bookItem && (
        <BookingForm patientId={plan.patient_id} planItemId={bookItem} treatmentCode={plan.items.find((i) => i.id === bookItem)?.treatment_code}
          onBooked={() => { setBookItem(undefined); onChange(); }} />
      )}
      {plan.versions.length > 1 && (
        <details><summary>Sürüm geçmişi ({plan.versions.length})</summary>
          <ul>{plan.versions.map((x) => <li key={x.version}>v{x.version} · {dateTime(x.created_at)} · net {tl(x.net_kurus)} · {x.approved_at ? "onaylı" : "onaysız"} {x.note}</li>)}</ul>
        </details>
      )}
      {installments.data && (
        <>
          <h3>Ödeme planı</h3>
          {installments.data.items.length === 0 ? (
            user.role === "accounting" && ["approved", "in_progress"].includes(plan.status) ? (
              <div className="inline">
                <Field label="Peşinat (₺)"><input value={inst.down} onChange={(e) => setInst({ ...inst, down: e.target.value })} /></Field>
                <Field label="Taksit sayısı"><input value={inst.count} onChange={(e) => setInst({ ...inst, count: e.target.value })} /></Field>
                <Field label="İlk vade"><input type="date" value={inst.first} onChange={(e) => setInst({ ...inst, first: e.target.value })} /></Field>
                <button onClick={() => act(() => api.post(`/api/plans/${plan.id}/installments`, {
                  down_payment_kurus: Math.round(Number(inst.down.replace(",", ".")) * 100), count: Number(inst.count), first_due_date: inst.first,
                }))}>Taksitlendir</button>
              </div>
            ) : <Empty>Taksit planı yok.</Empty>
          ) : (
            <table>
              <thead><tr><th>#</th><th>Vade</th><th className="r">Tutar</th><th className="r">Ödenen</th><th>Durum</th></tr></thead>
              <tbody>{installments.data.items.map((i) => (
                <tr key={i.id}><td>{i.seq === 0 ? "Peşinat" : i.seq}</td><td>{date(i.due_date)}</td><td className="r">{tl(i.amount_kurus)}</td><td className="r">{tl(i.paid_kurus)}</td><td><Badge status={i.status} /></td></tr>
              ))}</tbody>
            </table>
          )}
        </>
      )}
      {error && <ErrorBox error={error} />}
    </Card>
  );
}

interface Account { balance_kurus: number; charged_kurus: number; paid_kurus: number; refunded_kurus: number }
interface Ledger { id: number; kind: string; amount_kurus: number; balance_after: number; description: string; created_at: string }

const kindLabel: Record<string, string> = { charge: "Borç", payment: "Ödeme", refund: "İade", charge_reversal: "Borç iptali" };

function AccountTab({ patientId }: { patientId: number }) {
  const user = useUser()!;
  const account = useApi<Account>(`/api/patients/${patientId}/account`);
  const ledger = useApi<Page<Ledger>>(`/api/patients/${patientId}/ledger?limit=100`);
  const plans = useApi<{ items: Plan[] }>(user.role === "accounting" ? `/api/patients/${patientId}/plans` : null);
  const [amount, setAmount] = useState("");
  const [method, setMethod] = useState("card");
  const [planId, setPlanId] = useState("");
  const [error, setError] = useState<Error>();
  const [busy, setBusy] = useState(false);

  async function pay() {
    setBusy(true);
    setError(undefined);
    try {
      await api.post("/api/payments", { patient_id: patientId, plan_id: planId ? Number(planId) : null,
        amount_kurus: Math.round(Number(amount.replace(",", ".")) * 100), method }, true);
      setAmount("");
      account.reload();
      ledger.reload();
    } catch (e) { setError(e as Error); } finally { setBusy(false); }
  }
  return (
    <>
      <Async state={account}>
        {(a) => (
          <div className="stats">
            <div className="stat"><div className={`stat-v ${a.balance_kurus > 0 ? "debt" : ""}`}>{tl(a.balance_kurus)}</div><div className="stat-l">Bakiye (+ borç)</div></div>
            <div className="stat"><div className="stat-v">{tl(a.charged_kurus)}</div><div className="stat-l">Faturalanan</div></div>
            <div className="stat"><div className="stat-v">{tl(a.paid_kurus)}</div><div className="stat-l">Ödenen</div></div>
            <div className="stat"><div className="stat-v">{tl(a.refunded_kurus)}</div><div className="stat-l">İade</div></div>
          </div>
        )}
      </Async>
      {user.role === "accounting" && (
        <Card title="Ödeme al">
          <div className="inline">
            <Field label="Tutar (₺)"><input inputMode="decimal" value={amount} onChange={(e) => setAmount(e.target.value)} /></Field>
            <Field label="Yöntem"><select value={method} onChange={(e) => setMethod(e.target.value)}>
              <option value="card">Kart</option><option value="cash">Nakit</option><option value="transfer">Havale</option></select></Field>
            <Field label="Plan"><select value={planId} onChange={(e) => setPlanId(e.target.value)}>
              <option value="">Genel</option>
              {plans.data?.items.filter((p) => p.status !== "cancelled").map((p) => <option key={p.id} value={p.id}>Plan #{p.id}</option>)}
            </select></Field>
            <button className="primary" disabled={busy || !amount} onClick={pay}>Kaydet</button>
          </div>
          {error && <ErrorBox error={error} />}
        </Card>
      )}
      <Card title="Cari hesap hareketleri">
        <Async state={ledger} empty={(l) => l.items.length === 0}>
          {(l) => (
            <div className="table-wrap"><table>
              <thead><tr><th>Tarih</th><th>Tür</th><th>Açıklama</th><th className="r">Tutar</th><th className="r">Bakiye</th></tr></thead>
              <tbody>{l.items.map((e) => (
                <tr key={e.id}><td>{dateTime(e.created_at)}</td><td>{kindLabel[e.kind]}</td><td>{e.description}</td>
                  <td className={`r ${e.amount_kurus > 0 ? "debt" : "credit"}`}>{tl(e.amount_kurus)}</td><td className="r">{tl(e.balance_after)}</td></tr>
              ))}</tbody>
            </table></div>
          )}
        </Async>
      </Card>
    </>
  );
}

function SmsTab({ patientId }: { patientId: number }) {
  const list = useApi<{ items: { id: number; kind: string; body: string; created_at: string }[] }>(`/api/patients/${patientId}/notifications`, 10000);
  return (
    <Card title="Simüle SMS bildirimleri">
      <Async state={list} empty={(l) => l.items.length === 0}>
        {(l) => <ul className="feed">{l.items.map((n) => <li key={n.id}><span className="when">{dateTime(n.created_at)}</span><span>📱 {n.body}</span></li>)}</ul>}
      </Async>
    </Card>
  );
}
