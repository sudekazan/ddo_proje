// Thin fetch wrapper: attaches the access token, refreshes it once on 401 and turns the
// API's {"error":{code,message}} body into an ApiError.

export class ApiError extends Error {
  constructor(public status: number, public code: string, message: string, public details?: Record<string, string>) {
    super(message);
  }
}

type Session = { access: string; refresh: string; user: User };

export type Role = "patient" | "dentist" | "assistant" | "reception" | "accounting";

export interface User {
  user_id: number;
  role: Role;
  staff_id?: number;
  patient_id?: number;
  email: string;
  full_name: string;
}

const KEY = "dishekimim.session";
let session: Session | null = load();
const listeners = new Set<() => void>();

function load(): Session | null {
  try {
    const raw = localStorage.getItem(KEY);
    return raw ? (JSON.parse(raw) as Session) : null;
  } catch {
    return null;
  }
}

function save(s: Session | null) {
  session = s;
  try {
    if (s) localStorage.setItem(KEY, JSON.stringify(s));
    else localStorage.removeItem(KEY);
  } catch {
    /* storage unavailable: keep the session in memory only */
  }
  listeners.forEach((l) => l());
}

export const auth = {
  user: () => session?.user ?? null,
  subscribe(fn: () => void) {
    listeners.add(fn);
    return () => listeners.delete(fn);
  },
  async login(email: string, password: string) {
    const r = await request<{ access_token: string; refresh_token: string; user: User }>("POST", "/api/auth/login", { email, password }, false);
    save({ access: r.access_token, refresh: r.refresh_token, user: r.user });
  },
  async logout() {
    const s = session;
    save(null);
    if (s) await request("POST", "/api/auth/logout", { refresh_token: s.refresh }, false).catch(() => undefined);
  },
};

let refreshing: Promise<boolean> | null = null;

async function refresh(): Promise<boolean> {
  if (!session) return false;
  refreshing ??= (async () => {
    try {
      const r = await request<{ access_token: string; refresh_token: string; user: User }>(
        "POST", "/api/auth/refresh", { refresh_token: session!.refresh }, false);
      save({ access: r.access_token, refresh: r.refresh_token, user: r.user });
      return true;
    } catch {
      save(null);
      return false;
    } finally {
      refreshing = null;
    }
  })();
  return refreshing;
}

export function newIdempotencyKey() {
  return crypto.randomUUID ? crypto.randomUUID() : `${Date.now()}-${Math.random()}`;
}

export async function request<T = unknown>(method: string, path: string, body?: unknown, withAuth = true, headers: Record<string, string> = {}): Promise<T> {
  const h: Record<string, string> = { ...headers };
  if (body !== undefined) h["Content-Type"] = "application/json";
  if (withAuth && session) h["Authorization"] = `Bearer ${session.access}`;
  let res: Response;
  try {
    res = await fetch(path, { method, headers: h, body: body === undefined ? undefined : JSON.stringify(body) });
  } catch {
    throw new ApiError(0, "NETWORK", "Sunucuya ulaşılamadı");
  }
  if (res.status === 401 && withAuth && session) {
    if (await refresh()) return request<T>(method, path, body, withAuth, headers);
  }
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  const data = text ? JSON.parse(text) : undefined;
  if (!res.ok) {
    const e = data?.error ?? {};
    throw new ApiError(res.status, e.code ?? "HTTP_" + res.status, e.message ?? res.statusText, e.details);
  }
  return data as T;
}

export const api = {
  get: <T,>(path: string) => request<T>("GET", path),
  post: <T,>(path: string, body: unknown = {}, idempotent = false) =>
    request<T>("POST", path, body, true, idempotent ? { "Idempotency-Key": newIdempotencyKey() } : {}),
  patch: <T,>(path: string, body: unknown) => request<T>("PATCH", path, body),
  put: <T,>(path: string, body: unknown) => request<T>("PUT", path, body),
};
