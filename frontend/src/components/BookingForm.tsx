import { useState } from "react";
import { api } from "../api";
import { useApi } from "../hooks";
import { isoDay, time } from "../format";
import { Appointment, Slot, Staff, TreatmentType } from "../types";
import { Empty, ErrorBox, Field, Loading } from "./ui";

// BookingForm finds free slots (dentist + unit + assistant all free) and books one.
export function BookingForm({ patientId, planItemId, treatmentCode, tooth, onBooked }: {
  patientId?: number; planItemId?: number; treatmentCode?: string; tooth?: number | null; onBooked: (a: Appointment) => void;
}) {
  const types = useApi<{ items: TreatmentType[] }>("/api/treatment-types");
  const staff = useApi<{ items: Staff[] }>("/api/staff?kind=dentist");
  const tomorrow = new Date(Date.now() + 86400000);
  const [pid, setPid] = useState(patientId ? String(patientId) : "");
  const [code, setCode] = useState(treatmentCode ?? "EXAM");
  const [toothNo, setToothNo] = useState(tooth ? String(tooth) : "");
  const [dentist, setDentist] = useState("");
  const [day, setDay] = useState(isoDay(tomorrow));
  const [query, setQuery] = useState<string | null>(null);
  const slots = useApi<{ items: Slot[] }>(query);
  const [error, setError] = useState<Error>();
  const [busy, setBusy] = useState(false);
  const tt = types.data?.items.find((t) => t.code === code);

  async function book(s: Slot) {
    setBusy(true);
    setError(undefined);
    try {
      const body: Record<string, unknown> = { patient_id: Number(pid), dentist_id: s.dentist_id, start: s.start };
      if (planItemId) body.plan_item_id = planItemId;
      else body.treatment_code = code;
      if (!planItemId && tt?.requires_tooth) body.tooth = Number(toothNo);
      const a = await api.post<Appointment>("/api/appointments", body, true);
      onBooked(a);
      setQuery(null);
    } catch (e) {
      setError(e as Error);
      slots.reload(); // the slot may have been taken meanwhile
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="booking">
      <div className="grid4">
        {!patientId && <Field label="Hasta no"><input inputMode="numeric" value={pid} onChange={(e) => setPid(e.target.value)} placeholder="ör. 1" /></Field>}
        <Field label="Tedavi">
          <select value={code} onChange={(e) => setCode(e.target.value)} disabled={!!planItemId}>
            {types.data?.items.map((t) => <option key={t.code} value={t.code}>{t.name} ({t.duration_min} dk)</option>)}
          </select>
        </Field>
        {tt?.requires_tooth && !planItemId && (
          <Field label="Diş (FDI)"><input inputMode="numeric" value={toothNo} onChange={(e) => setToothNo(e.target.value)} placeholder="11-48 / 51-85" /></Field>
        )}
        <Field label="Hekim">
          <select value={dentist} onChange={(e) => setDentist(e.target.value)}>
            <option value="">Tümü</option>
            {staff.data?.items.map((d) => <option key={d.id} value={d.id}>{d.full_name}</option>)}
          </select>
        </Field>
        <Field label="Tarih"><input type="date" value={day} onChange={(e) => setDay(e.target.value)} /></Field>
      </div>
      <button className="primary" disabled={!pid} onClick={() => setQuery(`/api/availability?date=${day}&treatment_code=${code}${dentist ? `&dentist_id=${dentist}` : ""}`)}>
        Müsait saatleri göster
      </button>
      {error && <ErrorBox error={error} />}
      {query && (slots.loading ? <Loading /> : slots.error ? <ErrorBox error={slots.error} /> :
        slots.data && slots.data.items.length === 0 ? <Empty>Bu gün için boş saat yok.</Empty> : (
          <div className="slots">
            {slots.data?.items.slice(0, 80).map((s) => (
              <button key={`${s.dentist_id}-${s.start}`} className="slot" disabled={busy} onClick={() => book(s)}>
                {time(s.start)}<small>{s.dentist_name}</small>
              </button>
            ))}
          </div>
        ))}
    </div>
  );
}
