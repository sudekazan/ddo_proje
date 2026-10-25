import { useState } from "react";
import { api } from "../api";
import { useApi, useUser } from "../hooks";
import { Async, Card, ErrorBox } from "../components/ui";
import { BookingForm } from "../components/BookingForm";
import { isoDay } from "../format";
import { Appointment, Page, Staff } from "../types";
import { AppointmentTable } from "./PatientDetail";

// AppointmentActions shows only the actions the current role may perform.
export function AppointmentActions({ a, onChange }: { a: Appointment; onChange: () => void }) {
  const user = useUser()!;
  const [error, setError] = useState<Error>();
  const [busy, setBusy] = useState(false);
  async function act(path: string, body: unknown) {
    setBusy(true);
    setError(undefined);
    try { await api.post(path, body, true); onChange(); } catch (e) { setError(e as Error); } finally { setBusy(false); }
  }
  const mine = user.role !== "dentist" || user.staff_id === a.dentist_id;
  return (
    <div className="row-actions">
      {a.status === "booked" && (user.role === "reception" || user.role === "patient") && (
        <button className="link danger" disabled={busy} onClick={() => confirm("Randevu iptal edilsin mi?") && act(`/api/appointments/${a.id}/cancel`, { reason: "Arayüzden iptal" })}>İptal</button>
      )}
      {a.status === "booked" && (user.role === "assistant" || (user.role === "dentist" && mine)) && (
        <button className="link" disabled={busy} onClick={() => act(`/api/appointments/${a.id}/complete`, {})}>Tamamla</button>
      )}
      {a.status === "completed" && user.role === "dentist" && mine && (
        <button className="link danger" disabled={busy} onClick={() => {
          const reason = prompt("Geri alma gerekçesi");
          if (reason) act(`/api/appointments/${a.id}/revert`, { reason });
        }}>Geri al</button>
      )}
      {error && <ErrorBox error={error} />}
    </div>
  );
}

export function AppointmentsPage() {
  const user = useUser()!;
  const [day, setDay] = useState(isoDay(new Date()));
  const [dentist, setDentist] = useState(user.role === "dentist" ? String(user.staff_id) : "");
  const [booking, setBooking] = useState(false);
  const staff = useApi<{ items: Staff[] }>("/api/staff?kind=dentist");
  const list = useApi<Page<Appointment>>(`/api/appointments?date=${day}&limit=200${dentist ? `&dentist_id=${dentist}` : ""}`, 15000);
  const shift = (n: number) => { const d = new Date(day); d.setDate(d.getDate() + n); setDay(isoDay(d)); };
  return (
    <>
      <Card title="Randevu takvimi" actions={user.role === "reception" && <button className="primary" onClick={() => setBooking(!booking)}>+ Yeni randevu</button>}>
        {booking && <BookingForm onBooked={(a) => { setBooking(false); setDay(isoDay(new Date(a.start))); list.reload(); }} />}
        <div className="bar">
          <button onClick={() => shift(-1)} aria-label="Önceki gün">‹</button>
          <input type="date" value={day} onChange={(e) => setDay(e.target.value)} />
          <button onClick={() => shift(1)} aria-label="Sonraki gün">›</button>
          <button onClick={() => setDay(isoDay(new Date()))}>Bugün</button>
          <select value={dentist} onChange={(e) => setDentist(e.target.value)}>
            <option value="">Tüm hekimler</option>
            {staff.data?.items.map((d) => <option key={d.id} value={d.id}>{d.full_name}</option>)}
          </select>
        </div>
        <Async state={list} empty={(p) => p.items.length === 0}>
          {(p) => <AppointmentTable items={p.items} onChange={list.reload} showPatient />}
        </Async>
      </Card>
    </>
  );
}

interface Waiting { id: number; patient_id: number; patient_name: string; treatment_code: string; dentist_id: number | null; tooth: number | null; from: string; to: string; status: string; appointment_id: number | null; created_at: string }

export function WaitlistPage() {
  const [status, setStatus] = useState("waiting");
  const list = useApi<{ items: Waiting[] }>(`/api/waitlist?status=${status}`, 10000);
  const [f, setF] = useState({ patient_id: "", treatment_code: "EXAM", from: isoDay(new Date()), to: isoDay(new Date(Date.now() + 7 * 86400000)) });
  const [error, setError] = useState<Error>();
  async function add() {
    setError(undefined);
    try {
      await api.post("/api/waitlist", { patient_id: Number(f.patient_id), treatment_code: f.treatment_code,
        from: new Date(f.from + "T00:00:00").toISOString(), to: new Date(f.to + "T23:59:00").toISOString() });
      list.reload();
    } catch (e) { setError(e as Error); }
  }
  return (
    <>
      <Card title="Bekleme listesine ekle">
        <p className="muted">Bir randevu iptal edildiğinde, aralığı uyan ilk bekleyen hastaya aynı saat otomatik verilir.</p>
        <div className="inline">
          <label className="field"><span>Hasta no</span><input value={f.patient_id} onChange={(e) => setF({ ...f, patient_id: e.target.value })} /></label>
          <label className="field"><span>Tedavi</span><select value={f.treatment_code} onChange={(e) => setF({ ...f, treatment_code: e.target.value })}>
            <option value="EXAM">Muayene</option><option value="CLEANING">Diş taşı temizliği</option></select></label>
          <label className="field"><span>Başlangıç</span><input type="date" value={f.from} onChange={(e) => setF({ ...f, from: e.target.value })} /></label>
          <label className="field"><span>Bitiş</span><input type="date" value={f.to} onChange={(e) => setF({ ...f, to: e.target.value })} /></label>
          <button className="primary" onClick={add} disabled={!f.patient_id}>Ekle</button>
        </div>
        {error && <ErrorBox error={error} />}
      </Card>
      <Card title="Bekleme listesi" actions={
        <select value={status} onChange={(e) => setStatus(e.target.value)}>
          <option value="waiting">Bekleyen</option><option value="booked">Randevuya dönüşen</option><option value="">Tümü</option></select>}>
        <Async state={list} empty={(l) => l.items.length === 0}>
          {(l) => (
            <div className="table-wrap"><table>
              <thead><tr><th>Kayıt</th><th>Hasta</th><th>Tedavi</th><th>Aralık</th><th>Durum</th><th /></tr></thead>
              <tbody>{l.items.map((w) => (
                <tr key={w.id}>
                  <td>{new Date(w.created_at).toLocaleString("tr-TR")}</td><td>{w.patient_name} #{w.patient_id}</td><td>{w.treatment_code}</td>
                  <td>{new Date(w.from).toLocaleDateString("tr-TR")} – {new Date(w.to).toLocaleDateString("tr-TR")}</td>
                  <td>{w.status === "booked" ? `Randevu #${w.appointment_id}` : w.status}</td>
                  <td>{w.status === "waiting" && <button className="link danger" onClick={() => api.post(`/api/waitlist/${w.id}/cancel`).then(list.reload)}>Kaldır</button>}</td>
                </tr>
              ))}</tbody>
            </table></div>
          )}
        </Async>
      </Card>
    </>
  );
}
