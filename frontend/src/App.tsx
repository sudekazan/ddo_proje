import { useState } from "react";
import { BrowserRouter, Navigate, NavLink, Route, Routes } from "react-router-dom";
import { auth, Role } from "./api";
import { useUser } from "./hooks";
import { roleLabel } from "./format";
import { Login } from "./pages/Login";
import { DashboardPage } from "./pages/Dashboard";
import { PatientsPage } from "./pages/Patients";
import { PatientDetail } from "./pages/PatientDetail";
import { AppointmentsPage, WaitlistPage } from "./pages/Appointments";
import { InventoryPage } from "./pages/Inventory";
import { PricesPage, ReportsPage } from "./pages/Reports";

const staff: Role[] = ["dentist", "assistant", "reception", "accounting"];

// Each role only sees the screens of its job; the API enforces the same rules server side.
const nav: { to: string; label: string; roles: Role[] }[] = [
  { to: "/", label: "Panel", roles: staff },
  { to: "/me", label: "Bilgilerim", roles: ["patient"] },
  { to: "/appointments", label: "Randevular", roles: ["dentist", "assistant", "reception"] },
  { to: "/patients", label: "Hastalar", roles: staff },
  { to: "/waitlist", label: "Bekleme listesi", roles: ["reception"] },
  { to: "/inventory", label: "Stok", roles: ["assistant", "accounting", "dentist"] },
  { to: "/prices", label: "Fiyatlar", roles: ["accounting", "reception", "dentist"] },
  { to: "/reports", label: "Raporlar", roles: ["accounting", "dentist", "reception", "assistant"] },
];

function Shell() {
  const user = useUser()!;
  const [menu, setMenu] = useState(false);
  const items = nav.filter((n) => n.roles.includes(user.role));
  const allowed = (path: string) => items.some((n) => n.to === path);
  return (
    <div className="shell">
      <header className="top">
        <button className="burger" aria-label="Menü" onClick={() => setMenu(!menu)}>☰</button>
        <span className="brand">🦷 DişHekimim</span>
        <span className="who">{user.full_name} · {roleLabel[user.role]}</span>
        <button className="link" onClick={() => auth.logout()}>Çıkış</button>
      </header>
      <nav className={`side ${menu ? "open" : ""}`} onClick={() => setMenu(false)}>
        {items.map((n) => <NavLink key={n.to} to={n.to} end>{n.label}</NavLink>)}
      </nav>
      <main>
        <Routes>
          {user.role === "patient" ? (
            <>
              <Route path="/me" element={<PatientDetail id={user.patient_id} />} />
              <Route path="*" element={<Navigate to="/me" replace />} />
            </>
          ) : (
            <>
              <Route path="/" element={<DashboardPage />} />
              <Route path="/patients" element={<PatientsPage />} />
              <Route path="/patients/:id" element={<PatientDetail />} />
              {allowed("/appointments") && <Route path="/appointments" element={<AppointmentsPage />} />}
              {allowed("/waitlist") && <Route path="/waitlist" element={<WaitlistPage />} />}
              {allowed("/inventory") && <Route path="/inventory" element={<InventoryPage />} />}
              {allowed("/prices") && <Route path="/prices" element={<PricesPage />} />}
              {allowed("/reports") && <Route path="/reports" element={<ReportsPage />} />}
              <Route path="*" element={<Navigate to="/" replace />} />
            </>
          )}
        </Routes>
      </main>
    </div>
  );
}

export function App() {
  const user = useUser();
  return <BrowserRouter>{user ? <Shell /> : <Login />}</BrowserRouter>;
}
