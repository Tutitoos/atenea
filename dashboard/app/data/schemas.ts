import { z } from "zod";

const envelope = z.object({ data: z.unknown(), next_cursor: z.string().optional(), at: z.string().optional() });

export function parseEnvelope<T>(value: unknown): { data: T; next_cursor?: string; at?: string } {
  const parsed = envelope.safeParse(value);
  if (!parsed.success) throw new Error("Respuesta de dashboard inválida");
  return parsed.data as { data: T; next_cursor?: string; at?: string };
}

export const eventSchema = z.object({ seq: z.number().finite(), ts: z.string().optional(), kind: z.string().optional(), type: z.string().optional(), session_id: z.string().optional(), run_id: z.string().optional(), step_id: z.string().optional(), state: z.string().optional() }).passthrough();
