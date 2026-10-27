import { useCallback, useEffect, useRef, useState } from "react";
import { api, ApiError } from "./api";

export type Polled<T> = { data: T | null; error: ApiError | null; disconnected: boolean; reload: () => void };

// usePoll loads path now and every intervalMs. A failed request keeps the last data and flags
// "disconnected" when the server cannot be reached at all.
export function usePoll<T>(path: string | null, intervalMs = 2000): Polled<T> {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<ApiError | null>(null);
  const [disconnected, setDisconnected] = useState(false);
  const alive = useRef(true);
  const load = useCallback(async () => {
    if (path === null) return;
    try {
      const d = await api<T>(path);
      if (!alive.current) return;
      setData(d); setError(null); setDisconnected(false);
    } catch (e) {
      if (!alive.current) return;
      const err = e instanceof ApiError ? e : new ApiError(0, "unknown");
      setError(err);
      setDisconnected(err.status === 0 || err.status === 503);
    }
  }, [path]);
  useEffect(() => {
    alive.current = true;
    setData(null);
    void load();
    const id = setInterval(() => void load(), intervalMs);
    return () => { alive.current = false; clearInterval(id); };
  }, [load, intervalMs]);
  return { data, error, disconnected, reload: () => void load() };
}
