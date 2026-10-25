import { FormEvent, useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { api } from "../api";
import { useApi, useUser } from "../hooks";
import { Card, Empty, ErrorBox, Field, Loading } from "../components/ui";
import { tl } from "../format";
import { Page, Patient } from "../types";

export function PatientsPage() {
  const user = useUser();
  const [q, setQ] = useState("");
  const [items, setItems] = useState<Patient[]>([]);
  const [cursor, setCursor] = useState<string>();
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<Error>();
  const [showNew, setShowNew] = useState(false);

  async function load(reset: boolean, query = q) {
    setLoading(true);
    setError(undefined);
    try {
      const c = reset ? "" : `&cursor=${encodeURIComponent(cursor ?? "")}`;
      const page = await api.get<Page<Patient>>(`/api/patients?limit=25&q=${encodeURIComponent(query)}${c}`);
      setItems((prev) => (reset ? page.items : [...prev, ...page.items]));
      setCursor(page.next_cursor);
    } catch (e) {
      setError(e as Error);
    } finally {
      setLoading(false);
    }
  }
  useEffect(() => {
    const id = setTimeout(() => load(true, q), 250);
    return () => clearTimeout(id);
  }, [q]);

  return (
    <>
      <Card title="Hastalar" actions={user?.role === "reception" && <button className="primary" onClick={() => setShowNew(!showNew)}>+ Yeni hasta</button>}>
        {showNew && <NewPatient onDone={() => { setShowNew(false); load(true); }} />}
        <input className="search" placeholder="Soyadı veya 11 haneli TC kimlik no ile ara" value={q} onChange={(e) => setQ(e.target.value)} />
        {error && <ErrorBox error={error} onRetry={() => load(true)} />}
        {!loading && !error && items.length === 0 && <Empty>Eşleşen hasta yok.</Empty>}
        {items.length > 0 && (
          <div className="table-wrap">
            <table>
              <thead><tr><th>No</th><th>Ad Soyad</th><th>TC</th><th>Telefon</th><th>Kurum</th><th className="r">Bakiye</th></tr></thead>
              <tbody>
                {items.map((p) => (
                  <tr key={p.id}>
                    <td>{p.id}</td>
                    <td><Link to={`/patients/${p.id}`}>{p.first_name} {p.last_name}</Link></td>
                    <td>{p.national_id}</td>
                    <td>{p.phone}</td>
                    <td>{p.institution ?? "—"}</td>
                    <td className={`r ${p.balance_kurus > 0 ? "debt" : ""}`}>{tl(p.balance_kurus)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {loading && <Loading />}
        {cursor && !loading && <button className="more" onClick={() => load(false)}>Daha fazla yükle</button>}
      </Card>
    </>
  );
}

function NewPatient({ onDone }: { onDone: () => void }) {
  const nav = useNavigate();
  const inst = useApi<{ items: { id: number; name: string; discount_bp: number }[] }>("/api/institutions");
  const [f, setF] = useState({ national_id: "", first_name: "", last_name: "", birth_date: "", phone: "", email: "", institution_id: "" });
  const [error, setError] = useState<Error>();
  const set = (k: keyof typeof f) => (e: { target: { value: string } }) => setF({ ...f, [k]: e.target.value });

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(undefined);
    try {
      const p = await api.post<Patient>("/api/patients", {
        ...f, email: f.email || null, institution_id: f.institution_id ? Number(f.institution_id) : null,
      });
      onDone();
      nav(`/patients/${p.id}`);
    } catch (err) {
      setError(err as Error);
    }
  }
  return (
    <form className="subform" onSubmit={submit}>
      <div className="grid4">
        <Field label="TC kimlik no"><input value={f.national_id} onChange={set("national_id")} maxLength={11} required /></Field>
        <Field label="Ad"><input value={f.first_name} onChange={set("first_name")} required /></Field>
        <Field label="Soyad"><input value={f.last_name} onChange={set("last_name")} required /></Field>
        <Field label="Doğum tarihi"><input type="date" value={f.birth_date} onChange={set("birth_date")} required /></Field>
        <Field label="Telefon"><input value={f.phone} onChange={set("phone")} placeholder="05xxxxxxxxx" required /></Field>
        <Field label="E-posta"><input type="email" value={f.email} onChange={set("email")} /></Field>
        <Field label="Anlaşmalı kurum">
          <select value={f.institution_id} onChange={set("institution_id")}>
            <option value="">Yok</option>
            {inst.data?.items.map((i) => <option key={i.id} value={i.id}>{i.name} (%{i.discount_bp / 100})</option>)}
          </select>
        </Field>
      </div>
      {error && <ErrorBox error={error} />}
      <button className="primary">Kaydet</button>
    </form>
  );
}
