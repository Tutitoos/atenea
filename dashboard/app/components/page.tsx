import { Link } from "react-router";
import { useState } from "react";
import { ArrowUpRight, Clock3, Database, Gauge, Layers3, RefreshCw, ShieldCheck, Wallet, Zap } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { api } from "~/data/api";
import { keys } from "~/data/query";
import type { Overview, Session, State } from "~/lib/types";
import { Card, CardContent, CardHeader, CardTitle } from "./ui/card";
import { Button } from "./ui/button";
import { Badge } from "./ui/badge";
import { Skeleton } from "./ui/skeleton";
import { StateBadge, StateView } from "./status";

export function PageHeader({ eyebrow, title, description, children }: { eyebrow: string; title: string; description: string; children?: React.ReactNode }) {
  return <header className="mb-6 flex flex-col justify-between gap-4 sm:flex-row sm:items-end"><div><p className="text-[11px] font-semibold uppercase tracking-[0.2em] text-primary">{eyebrow}</p><h1 className="mt-1 text-2xl font-semibold tracking-tight sm:text-3xl">{title}</h1><p className="mt-2 max-w-2xl text-sm text-muted-foreground">{description}</p></div>{children}</header>;
}

export function RangeControl({ value, onChange }: { value: string; onChange: (value: string) => void }) {
  return <div className="inline-flex rounded-lg border bg-card p-1" aria-label="Intervalo">{["1h", "24h", "7d", "30d"].map((item) => <button key={item} className={`min-h-9 rounded-md px-3 text-xs font-medium ${item === value ? "bg-primary text-primary-foreground" : "text-muted-foreground hover:bg-accent"}`} onClick={() => onChange(item)}>{item}</button>)}</div>;
}

export function formatNumber(value: unknown, fallback = "No medido") { return typeof value === "number" && Number.isFinite(value) ? value.toLocaleString("es-ES") : fallback; }
export function formatPercent(value: unknown) { return typeof value === "number" && Number.isFinite(value) && value >= 0 ? `${Math.round(Math.min(100, value))}%` : "No medido"; }
export function formatDuration(value: unknown) { return typeof value === "number" && Number.isFinite(value) && value >= 0 ? value < 1000 ? `${Math.round(value)} ms` : `${(value / 1000).toLocaleString("es-ES", { maximumFractionDigits: 1 })} s` : "No medido"; }
export function relativeDate(value?: string) { if (!value) return "Fecha desconocida"; const time = Date.parse(value); if (!Number.isFinite(time)) return "Fecha observada"; const delta = Math.max(0, Date.now() - time); if (delta < 60_000) return "Hace menos de 1 min"; if (delta < 3_600_000) return `Hace ${Math.floor(delta / 60_000)} min`; return new Date(time).toLocaleString("es-ES", { dateStyle: "short", timeStyle: "short" }); }

export function MetricCard({ label, value, hint, icon: Icon = Gauge, tone = "" }: { label: string; value: string; hint: string; icon?: typeof Gauge; tone?: "success" | "warning" | "danger" | "" }) {
  return <Card className="min-w-0"><CardContent className="p-4"><div className="flex items-start justify-between gap-3"><div><p className="text-xs text-muted-foreground">{label}</p><p className={`mt-2 text-2xl font-semibold tracking-tight ${tone === "success" ? "text-[color:var(--status-success)]" : tone === "warning" ? "text-[color:var(--status-warning)]" : tone === "danger" ? "text-[color:var(--status-danger)]" : ""}`}>{value}</p><p className="mt-1 text-[11px] text-muted-foreground">{hint}</p></div><span className="grid size-9 shrink-0 place-items-center rounded-lg bg-accent text-primary"><Icon className="size-4" /></span></div></CardContent></Card>;
}

function overviewValue(data: Overview | undefined, key: "sessions" | "runs") {
  const value = data?.[key];
  if (typeof value === "number" && Number.isFinite(value)) return value;
  if (Array.isArray(value)) return value.length;
  if (value && typeof value === "object" && typeof (value as { total?: unknown }).total === "number") return (value as { total: number }).total;
  const fallback = data?.stats?.[key];
  return typeof fallback === "number" && Number.isFinite(fallback) ? fallback : undefined;
}

export function OverviewPage() {
  const [range, setRange] = useUrlRange();
  const overview = useQuery({ queryKey: keys.overview(range), queryFn: () => api.overview(range) });
  const sessions = useQuery({ queryKey: keys.sessions({ range }), queryFn: () => api.sessions({ range }) });
  const data = overview.data?.data;
  const sessionItems = sessions.data?.data?.items || [];
  const stats = data?.stats || {};
  const active = sessionItems.filter((item) => item.active || item.state === "running");
  return <div><PageHeader eyebrow="Project Atlas · overview" title="Estado del sistema" description="Operación de Atenea reconstruida desde la actividad durable y actualizada en tiempo real."><RangeControl value={range} onChange={setRange} /></PageHeader><StateView loading={overview.isLoading} error={overview.isError} empty={!overview.isLoading && !data}><div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4"><MetricCard label="Sesiones activas" value={formatNumber(active.length, "0")} hint={`${formatNumber(overviewValue(data, "sessions"), "No medido")} en el intervalo`} icon={Layers3} /><MetricCard label="Runs" value={formatNumber(overviewValue(data, "runs"))} hint={`Intervalo ${range}`} icon={Zap} /><MetricCard label="Éxito" value={formatPercent(stats.success_rate)} hint="Intentos con resultado medido" icon={ShieldCheck} tone="success" /><MetricCard label="Duración media" value={formatDuration(stats.duration_ms)} hint="Solo intentos detallados" icon={Clock3} /><MetricCard label="Tokens" value={formatNumber(stats.tokens)} hint="Cobertura separada" icon={Database} /><MetricCard label="Coste conocido" value={typeof stats.cost === "number" ? `$${stats.cost.toFixed(2)}` : "No medido"} hint="No se estiman faltantes" icon={Wallet} /><MetricCard label="Estado global" value={String(data?.snapshot?.light || "unknown").toUpperCase()} hint="Estado del servicio" icon={Gauge} tone={data?.snapshot?.light === "green" ? "success" : data?.snapshot?.light === "amber" ? "warning" : ""} /><MetricCard label="Cobertura" value={formatPercent((stats as { coverage?: number }).coverage)} hint="Medición completa" icon={RefreshCw} /></div><div className="mt-4 grid gap-4 xl:grid-cols-[1.15fr_.85fr]"><Card><CardHeader className="flex-row items-center justify-between"><div><CardTitle>Sesiones activas</CardTitle><p className="mt-1 text-xs text-muted-foreground">Última actividad observada</p></div><Link className="text-xs text-primary hover:underline" to="/sessions">Ver historial</Link></CardHeader><CardContent className="pt-0"><StateView loading={sessions.isLoading} error={sessions.isError} empty={!sessionItems.length}>{<div className="divide-y">{(active.length ? active : sessionItems.slice(0, 5)).map((session) => <SessionRow key={session.id} session={session} />)}</div>}</StateView></CardContent></Card><FlowSummary overview={data} /></div></StateView></div>;
}

function SessionRow({ session }: { session: Session }) { return <Link to={`/sessions/${encodeURIComponent(session.id)}`} className="flex min-h-16 items-center justify-between gap-4 py-3 hover:bg-accent/40"><div className="flex min-w-0 items-center gap-3"><span className="size-2 shrink-0 rounded-full bg-[color:var(--status-success)]" /><div className="min-w-0"><p className="truncate text-sm font-medium">{session.name || "Sesión sin nombre"}</p><p className="truncate text-xs text-muted-foreground">{session.primary_project || "Proyecto desconocido"} · {session.origin?.client || "Cliente desconocido"}</p></div></div><div className="hidden shrink-0 items-center gap-3 text-right sm:flex"><StateBadge value={session.state || (session.active ? "active" : "unknown")} /><span className="text-xs text-muted-foreground">{relativeDate(session.updated_at)}</span></div></Link>; }

function FlowSummary({ overview }: { overview?: Overview }) { return <Card><CardHeader><CardTitle>Flujo de herramientas y proveedores</CardTitle><p className="mt-1 text-xs text-muted-foreground">Resumen de actividad observada</p></CardHeader><CardContent><div className="grid gap-3 sm:grid-cols-3"><div className="rounded-lg border bg-background p-4"><p className="text-xs text-muted-foreground">Entrada</p><p className="mt-2 text-lg font-semibold">{formatNumber(overviewValue(overview, "sessions"))}</p><p className="text-xs text-muted-foreground">sesiones</p></div><div className="rounded-lg border bg-background p-4"><p className="text-xs text-muted-foreground">Ejecución</p><p className="mt-2 text-lg font-semibold">{formatNumber(overviewValue(overview, "runs"))}</p><p className="text-xs text-muted-foreground">runs</p></div><div className="rounded-lg border bg-background p-4"><p className="text-xs text-muted-foreground">Provider</p><p className="mt-2 text-lg font-semibold">{overview?.snapshot?.light ? String(overview.snapshot.light) : "Unknown"}</p><p className="text-xs text-muted-foreground">estado global</p></div></div><div className="mt-5 flex items-center gap-2 text-xs text-muted-foreground"><span className="size-2 rounded-full bg-[color:var(--status-success)]" />Solicitudes observadas<span className="mx-1 text-border">→</span><span className="rounded-md border px-2 py-1">Selector</span><span className="text-border">→</span><span className="rounded-md border px-2 py-1">Tool / provider</span></div></CardContent></Card>; }

export function useUrlRange() { const [value, setValue] = useStateFromSearch("range", "24h"); return [value, setValue] as const; }
function setSearchValue(key: string, fallback: string) { if (typeof window === "undefined") return fallback; return new URLSearchParams(window.location.search).get(key) || fallback; }
function useStateFromSearch(key: string, fallback: string) { const [value, setValue] = useState(() => setSearchValue(key, fallback)); const update = (next: string) => { setValue(next); const params = new URLSearchParams(window.location.search); params.set(key, next); window.history.replaceState(null, "", `${window.location.pathname}?${params}`); }; return [value, update] as const; }
