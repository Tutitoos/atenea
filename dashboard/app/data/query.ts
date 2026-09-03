import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef } from "react";
import { connectEvents } from "./api";
import type { EventRecord } from "~/lib/types";

export const keys = {
  overview: (range: string) => ["overview", range] as const,
  sessions: (filters: Record<string, string | undefined>) => ["sessions", filters] as const,
  session: (id: string) => ["session", id] as const,
  runs: (filters: Record<string, string | undefined>) => ["runs", filters] as const,
  run: (id: string) => ["run", id] as const,
  metrics: (filters: Record<string, string | undefined>) => ["metrics", filters] as const,
  catalog: ["catalog"] as const,
  incidents: ["incidents"] as const,
};

export function useRealtime() {
  const client = useQueryClient();
  const lastSeq = useRef(0);
  useEffect(() => {
    let stopped = false;
    let close: () => void = () => undefined;
    let reconnectTimer: number | undefined;
    const connect = (after: number) => {
      if (stopped) return;
      close = connectEvents(after, {
        event: (event: EventRecord) => {
          if (event.seq <= lastSeq.current) return;
          lastSeq.current = event.seq;
          if (event.session_id) void client.invalidateQueries({ queryKey: ["session", event.session_id] });
          void client.invalidateQueries({ queryKey: ["overview"] });
          void client.invalidateQueries({ queryKey: ["sessions"] });
          void client.invalidateQueries({ queryKey: ["runs"] });
        },
        reset: () => {
          // A reset means the in-memory hub no longer covers our cursor (or
          // the service restarted). Rebuild the durable projection and start
          // a fresh stream so the next event cannot be missed between those
          // two operations.
          lastSeq.current = 0;
          void client.invalidateQueries();
          close();
          window.dispatchEvent(new CustomEvent("atenea:connection", { detail: { connected: false } }));
          if (!stopped) {
            reconnectTimer = window.setTimeout(() => connect(0), 0);
          }
        },
        status: (connected) => {
          window.dispatchEvent(new CustomEvent("atenea:connection", { detail: { connected } }));
        },
      });
    };
    connect(lastSeq.current);
    return () => {
      stopped = true;
      if (reconnectTimer !== undefined) window.clearTimeout(reconnectTimer);
      close();
    };
  }, [client]);
}
