import { AlertTriangle, CheckCircle2, CircleHelp, LoaderCircle, WifiOff } from "lucide-react";
import { Badge } from "./ui/badge";
import { Skeleton } from "./ui/skeleton";
import type { State } from "~/lib/types";

export function StateBadge({ value }: { value?: State }) {
  const normalized = String(value || "unknown").toLowerCase();
  const variant = normalized === "green" || normalized === "ok" || normalized === "success" || normalized === "active" || normalized === "running" || normalized === "healthy" ? "success" : normalized === "amber" || normalized === "partial" || normalized === "degraded" || normalized === "warning" ? "warning" : normalized === "red" || normalized === "failed" || normalized === "error" ? "danger" : "outline";
  return <Badge variant={variant}><span className="mr-1 size-1.5 rounded-full bg-current" aria-hidden="true" />{normalized.toUpperCase()}</Badge>;
}

export function StateView({ loading, error, empty, children }: { loading?: boolean; error?: boolean; empty?: boolean; children: React.ReactNode }) {
  if (loading) return <div className="space-y-3" role="status" aria-label="Cargando"><Skeleton className="h-24 w-full" /><Skeleton className="h-32 w-full" /></div>;
  if (error) return <div className="flex items-center gap-3 rounded-lg border border-[color:var(--status-danger)]/30 bg-[color:var(--status-danger)]/10 p-4 text-sm"><WifiOff className="size-4" /> Datos no disponibles. La vista conserva la última información conocida.</div>;
  if (empty) return <div className="grid min-h-40 place-items-center rounded-lg border border-dashed p-8 text-center text-sm text-muted-foreground"><CircleHelp className="mb-2 size-5" /><span>No hay datos para este intervalo.</span></div>;
  return <>{children}</>;
}

export function LiveDot({ state = "green" }: { state?: State }) { return <span className="relative flex size-2" aria-label="En vivo"><span className={`absolute inline-flex size-full animate-ping rounded-full opacity-60 ${state === "amber" ? "bg-[color:var(--status-warning)]" : "bg-[color:var(--status-success)]"}`} /><span className={`relative inline-flex size-2 rounded-full ${state === "amber" ? "bg-[color:var(--status-warning)]" : "bg-[color:var(--status-success)]"}`} /></span>; }
