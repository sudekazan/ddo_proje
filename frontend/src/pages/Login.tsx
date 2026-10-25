import { FormEvent, useState } from "react";
import { auth } from "../api";
import { ErrorBox } from "../components/ui";

const demo = [
  ["Resepsiyon", "resepsiyon@dishekimim.local"],
  ["Diş hekimi", "hekim@dishekimim.local"],
  ["Asistan", "asistan@dishekimim.local"],
  ["Muhasebe", "muhasebe@dishekimim.local"],
  ["Hasta", "hasta@dishekimim.local"],
];

export function Login() {
  const [email, setEmail] = useState("resepsiyon@dishekimim.local");
  const [password, setPassword] = useState("Demo123!");
  const [error, setError] = useState<Error>();
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      await auth.login(email, password);
    } catch (err) {
      setError(err as Error);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="login">
      <form className="card login-card" onSubmit={submit}>
        <h1>🦷 DişHekimim</h1>
        <p className="muted">Diş kliniği randevu ve tedavi planı sistemi</p>
        <label className="field"><span>E-posta</span>
          <input type="email" value={email} onChange={(e) => setEmail(e.target.value)} autoComplete="username" required /></label>
        <label className="field"><span>Parola</span>
          <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" required /></label>
        {error && <ErrorBox error={error} />}
        <button className="primary" disabled={busy}>{busy ? "Giriş yapılıyor…" : "Giriş yap"}</button>
        <div className="demo">
          <span className="muted">Demo hesaplar (parola: Demo123!)</span>
          <div className="chips">
            {demo.map(([label, mail]) => (
              <button type="button" key={mail} className={`chip ${email === mail ? "on" : ""}`} onClick={() => { setEmail(mail); setPassword("Demo123!"); }}>{label}</button>
            ))}
          </div>
        </div>
      </form>
    </div>
  );
}
