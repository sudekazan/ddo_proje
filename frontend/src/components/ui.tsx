import { ReactNode } from "react";
import { ApiError } from "../api";
import { statusLabel } from "../format";

export function Loading({ label = "Yükleniyor…" }: { label?: string }) {
  return <div className="state" role="status"><span className="spinner" aria-hidden /> {label}</div>;
}

export function Empty({ children }: { children: ReactNode }) {
  return <div className="state empty">{children}</div>;
}

export function ErrorBox({ error, onRetry }: { error: Error; onRetry?: () => void }) {
  const e = error as ApiError;
  return (
    <div className="error" role="alert">
      <strong>{e.code && e.code !== "NETWORK" ? e.code : "Hata"}</strong>: {e.message}
      {e.details && (
        <ul>{Object.entries(e.details).map(([k, v]) => <li key={k}><code>{k}</code>: {v}</li>)}</ul>
      )}
      {onRetry && <button className="link" onClick={onRetry}>Tekrar dene</button>}
    </div>
  );
}

// Section renders the loading / error / empty states of a Loadable consistently.
export function Async<T>({ state, empty, children }: {
  state: { data?: T; error?: Error; loading: boolean; reload: () => void };
  empty?: (d: T) => boolean;
  children: (d: T) => ReactNode;
}) {
  if (state.loading) return <Loading />;
  if (state.error && state.data === undefined) return <ErrorBox error={state.error} onRetry={state.reload} />;
  if (state.data === undefined) return null;
  if (empty?.(state.data)) return <Empty>Kayıt bulunamadı.</Empty>;
  return <>{children(state.data)}</>;
}

export function Badge({ status }: { status: string }) {
  return <span className={`badge b-${status}`}>{statusLabel[status] ?? status}</span>;
}

export function Card({ title, actions, children }: { title?: ReactNode; actions?: ReactNode; children: ReactNode }) {
  return (
    <section className="card">
      {(title || actions) && <header className="card-h"><h2>{title}</h2><div className="actions">{actions}</div></header>}
      {children}
    </section>
  );
}

export function Field({ label, children }: { label: string; children: ReactNode }) {
  return <label className="field"><span>{label}</span>{children}</label>;
}

export function Stat({ label, value, hint }: { label: string; value: ReactNode; hint?: string }) {
  return <div className="stat"><div className="stat-v">{value}</div><div className="stat-l">{label}</div>{hint && <div className="stat-h">{hint}</div>}</div>;
}
