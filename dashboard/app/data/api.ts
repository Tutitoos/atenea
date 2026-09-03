import type { Envelope, EventRecord, Overview, Run, Session } from "~/lib/types";
import { parseEnvelope, eventSchema } from "./schemas";

export class UnauthorizedError extends Error {}

async function request<T>(path: string, init?: RequestInit): Promise<Envelope<T>> {
  const response = await fetch(path, { credentials: "same-origin", ...init });
  if (response.status === 401) {
    if (typeof window !== "undefined") window.dispatchEvent(new Event("atenea:auth-required"));
    throw new UnauthorizedError("Autenticación requerida");
  }
  if (!response.ok) throw new Error((await response.text()).slice(0, 240) || `HTTP ${response.status}`);
  return parseEnvelope<T>(await response.json()) as Envelope<T>;
}

const query = (values: Record<string, string | undefined>) => {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(values)) if (value) params.set(key, value);
  return params.toString();
};

export const api = {
  overview: (range: string) => request<Overview>(`/api/v1/overview?${query({ range, limit: "100" })}`),
  sessions: (values: Record<string, string | undefined> = {}) => request<{ items?: Session[]; total?: number }>(`/api/v1/sessions?${query({ limit: "100", ...values })}`),
  session: (id: string) => request<Session>(`/api/v1/sessions/${encodeURIComponent(id)}`),
  runs: (values: Record<string, string | undefined> = {}) => request<{ items?: Run[]; total?: number }>(`/api/v1/runs?${query({ limit: "100", ...values })}`),
  run: (id: string) => request<Run>(`/api/v1/runs/${encodeURIComponent(id)}`),
  metrics: (values: Record<string, string | undefined> = {}) => request<unknown>(`/api/v1/metrics?${query({ limit: "100", ...values })}`),
  incidents: () => request<unknown>("/api/v1/incidents?limit=100"),
  catalog: () => request<unknown>("/api/v1/catalog"),
  login: async (token: string) => {
    // Login intentionally is the one non-envelope endpoint: the server only
    // returns `{ok:true}` and sets an HttpOnly cookie. Keep the token in this
    // call stack; it is never copied to Query/localStorage or an event.
    const response = await fetch("/api/v1/auth/login", { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ token }) });
    if (response.status === 401) {
      if (typeof window !== "undefined") window.dispatchEvent(new Event("atenea:auth-required"));
      throw new UnauthorizedError("Token no válido");
    }
    if (!response.ok) throw new Error((await response.text()).slice(0, 240) || `HTTP ${response.status}`);
    const body = await response.json() as { ok?: boolean };
    if (body.ok !== true) throw new Error("Respuesta de login inválida");
    return { data: { ok: true } };
  },
};

export function connectEvents(after: number, handlers: { event: (event: EventRecord) => void; reset: () => void; status: (connected: boolean) => void }) {
  const source = new EventSource(`/api/v1/events${after ? `?after=${encodeURIComponent(after)}` : ""}`);
  const onEvent = (message: MessageEvent<string>) => {
    try {
      const parsed = eventSchema.safeParse(JSON.parse(message.data));
      if (parsed.success) handlers.event(parsed.data as EventRecord);
    } catch { /* invalid events are ignored safely */ }
  };
  source.onopen = () => handlers.status(true);
  source.onerror = () => handlers.status(false);
  source.addEventListener("reset", () => handlers.reset());
  // The Go hub keeps the concrete lifecycle kind in the SSE event name
  // (for example `run.started`) so the stream remains useful to non-React
  // consumers too. EventSource does not provide a wildcard listener; keep
  // the base categories and the concrete kinds used by Atenea registered so
  // every update reaches the idempotent query projection.
  for (const kind of [
    "session", "session.opened", "session.closed",
    "run", "run.started", "run.closed",
    "step", "step.started", "step.closed",
    "selector", "selector.completed",
    "provider", "provider.started", "provider.completed",
    "retry", "retry.started", "retry.completed",
    "gate", "gate.waiting", "gate.completed",
    "health", "health.index", "health.server",
    "process", "process.status",
    "maintenance", "maintenance.completed",
    "incident", "tool", "tool.started", "tool.completed",
  ]) source.addEventListener(kind, onEvent);
  return () => source.close();
}
