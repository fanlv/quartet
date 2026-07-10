import { createContext, useContext, useState, useEffect, useCallback, useRef, ReactNode } from 'react';

interface ConnectionStatusContextType {
  connected: boolean;
  buildTime: string;
  /** Call when SSE stream disconnects unexpectedly */
  reportDisconnect: () => void;
  /** Call when SSE stream reconnects successfully */
  reportReconnect: () => void;
}

const ConnectionStatusContext = createContext<ConnectionStatusContextType>({
  connected: true,
  buildTime: '',
  reportDisconnect: () => {},
  reportReconnect: () => {},
});

// The context hook lives alongside the provider so a single import covers
// both; disable the hot-reload warning here rather than splitting the module.
// eslint-disable-next-line react-refresh/only-export-components
export function useConnectionStatus() {
  return useContext(ConnectionStatusContext);
}

const HEALTH_URL = '/api/v1/health';
const POLL_INTERVAL = 5000;

export function ConnectionStatusProvider({ children }: { children: ReactNode }) {
  const [connected, setConnected] = useState(true);
  const [buildTime, setBuildTime] = useState('');
  const pollingRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const mountedRef = useRef(true);

  const stopPolling = useCallback(() => {
    if (pollingRef.current) {
      clearInterval(pollingRef.current);
      pollingRef.current = null;
    }
  }, []);

  const checkHealth = useCallback(async (): Promise<boolean> => {
    try {
      const res = await fetch(HEALTH_URL, { method: 'GET', signal: AbortSignal.timeout(3000) });
      if (!res.ok) return false;

      try {
        const body = await res.json() as { buildTime?: unknown };
        if (typeof body.buildTime === 'string' && body.buildTime) {
          setBuildTime(body.buildTime);
        }
      } catch {
        // An older backend may return a non-JSON health body. It is still up.
      }
      return true;
    } catch {
      return false;
    }
  }, []);

  const startPolling = useCallback(() => {
    if (pollingRef.current) return;
    pollingRef.current = setInterval(async () => {
      const ok = await checkHealth();
      if (!mountedRef.current) return;
      if (ok) {
        // Backend recovered. Let the SSE client's own reconnect drive message
        // re-sync via syncJobState — don't reload the page (would lose the
        // composer's draft text).
        setConnected(true);
        stopPolling();
      }
    }, POLL_INTERVAL);
  }, [checkHealth, stopPolling]);

  const reportDisconnect = useCallback(() => {
    setConnected(false);
    startPolling();
  }, [startPolling]);

  const reportReconnect = useCallback(() => {
    setConnected(true);
    stopPolling();
    void checkHealth();
  }, [checkHealth, stopPolling]);

  // Initial health check on mount
  useEffect(() => {
    mountedRef.current = true;
    checkHealth().then((ok) => {
      if (!mountedRef.current) return;
      if (!ok) {
        setConnected(false);
        startPolling();
      }
    });
    return () => {
      mountedRef.current = false;
      stopPolling();
    };
  }, [checkHealth, startPolling, stopPolling]);

  return (
    <ConnectionStatusContext.Provider value={{ connected, buildTime, reportDisconnect, reportReconnect }}>
      {children}
    </ConnectionStatusContext.Provider>
  );
}
