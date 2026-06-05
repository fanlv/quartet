import { createContext, useContext, ReactNode } from 'react';

// Returns the best estimate of current server wall-clock time (ms since epoch).
// The estimate is produced by tracking max(event.timestamp) against the client
// receive time in the event pipeline (see useJobChat.getServerNow), so that
// live duration ticks compute `serverNow - startedAt` — same reference frame
// as the backend's own `endedAt - startedAt` on terminal events. Without this
// alignment, client/server clock skew, SSE delivery latency, and ring-buffer
// replay all inflate the live value and produce a visible "running 3m → final
// 300ms" jump on completion.
export type GetServerNow = () => number;

const defaultGetServerNow: GetServerNow = () => Date.now();

const ServerClockContext = createContext<GetServerNow>(defaultGetServerNow);

export function ServerClockProvider({ getServerNow, children }: { getServerNow: GetServerNow; children: ReactNode }) {
  return <ServerClockContext.Provider value={getServerNow}>{children}</ServerClockContext.Provider>;
}

// eslint-disable-next-line react-refresh/only-export-components
export function useServerNow(): GetServerNow {
  return useContext(ServerClockContext);
}
