import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
import { api, auth } from "./api";

export function useUser() {
  return useSyncExternalStore(auth.subscribe, auth.user);
}

export interface Loadable<T> {
  data: T | undefined;
  error: Error | undefined;
  loading: boolean;
  reload: () => void;
}

// useApi loads path (null = don't load) and optionally polls it every pollMs.
export function useApi<T>(path: string | null, pollMs?: number): Loadable<T> {
  const [data, setData] = useState<T>();
  const [error, setError] = useState<Error>();
  const [loading, setLoading] = useState(!!path);
  const [tick, setTick] = useState(0);
  const current = useRef(path);
  current.current = path;

  useEffect(() => {
    if (!path) {
      setLoading(false);
      return;
    }
    let cancelled = false;
    setLoading(true);
    api.get<T>(path)
      .then((d) => { if (!cancelled && current.current === path) { setData(d); setError(undefined); } })
      .catch((e: Error) => { if (!cancelled) setError(e); })
      .finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [path, tick]);

  useEffect(() => {
    if (!pollMs || !path) return;
    const id = setInterval(() => setTick((t) => t + 1), pollMs);
    return () => clearInterval(id);
  }, [pollMs, path]);

  const reload = useCallback(() => setTick((t) => t + 1), []);
  return { data, error, loading: loading && data === undefined, reload };
}

export function useAction<A extends unknown[], R>(fn: (...args: A) => Promise<R>) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<Error>();
  const run = useCallback(async (...args: A): Promise<R | undefined> => {
    setBusy(true);
    setError(undefined);
    try {
      return await fn(...args);
    } catch (e) {
      setError(e as Error);
      return undefined;
    } finally {
      setBusy(false);
    }
  }, [fn]);
  return { run, busy, error, clear: () => setError(undefined) };
}
