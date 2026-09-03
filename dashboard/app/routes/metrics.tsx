import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { CartesianGrid, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { BarChart3, Clock3, Coins, Gauge, MemoryStick, ShieldCheck } from "lucide-react";
import { api } from "~/data/api";
import { keys } from "~/data/query";
import { Card, CardContent, CardHeader, CardTitle } from "~/components/ui/card";
import { MetricCard, PageHeader, RangeControl, formatDuration, formatNumber, formatPercent, useUrlRange } from "~/components/page";
import { StateView } from "~/components/status";

export default function MetricsRoute() {
  const [range, setRange] = useUrlRange();
  const summary = useQuery({ queryKey: keys.metrics({ range, view: "summary" }), queryFn: () => api.metrics({ range, view: "summary" }) });
  const series = useQuery({ queryKey: keys.metrics({ range, view: "series", bucket: range === "1h" ? "1m" : "15m" }), queryFn: () => api.metrics({ range, view: "series", bucket: range === "1h" ? "1m" : "15m" }) });
  const payload = asRecord(summary.data?.data);
  const stats = asRecord(payload.stats || payload);
  const points = useMemo(() => {
    const value = series.data?.data;
    const rows = Array.isArray(value) ? value : asRecord(value).items;
    if (!Array.isArray(rows)) return [];
    return rows.filter((row): row is Record<string, unknown> => Boolean(row && typeof row === "object")).map((row) => {
      const attempts = typeof row.attempts === "number" && Number.isFinite(row.attempts) ? row.attempts : 0;
      const successes = typeof row.successes === "number" && Number.isFinite(row.successes) ? row.successes : 0;
      return { ...row, success_rate: attempts > 0 ? (successes * 100) / attempts : undefined };
    });
  }, [series.data]);
  return <div><PageHeader eyebrow="Health Scorecard" title="Metrics" description="Fiabilidad, rendimiento, eficiencia y cobertura. Solo se muestran percentiles respaldados por intentos detallados."><RangeControl value={range} onChange={setRange} /></PageHeader><StateView loading={summary.isLoading} error={summary.isError} empty={!summary.data}><div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4"><MetricCard label="Éxito" value={formatPercent(stats.success_rate)} hint="Intentos medidos" icon={ShieldCheck} tone="success" /><MetricCard label="Duración media" value={formatDuration(stats.duration_ms)} hint="Milisegundos observados" icon={Clock3} /><MetricCard label="Tokens" value={formatNumber(stats.tokens)} hint="Sin estimaciones" icon={Gauge} /><MetricCard label="Coste conocido" value={typeof stats.cost === "number" ? `$${stats.cost.toFixed(2)}` : "No medido"} hint="Cobertura explícita" icon={Coins} /></div><div className="mt-4 grid gap-4 lg:grid-cols-[1.3fr_.7fr]"><Card><CardHeader><CardTitle>Series temporales</CardTitle></CardHeader><CardContent><StateView loading={series.isLoading} error={series.isError} empty={!points.length}><div className="h-72 w-full"><ResponsiveContainer width="100%" height="100%"><LineChart data={points}><CartesianGrid strokeDasharray="3 3" stroke="var(--border)" /><XAxis dataKey="bucket" tick={{ fill: "var(--muted-foreground)", fontSize: 11 }} /><YAxis tick={{ fill: "var(--muted-foreground)", fontSize: 11 }} /><Tooltip contentStyle={{ background: "var(--card)", borderColor: "var(--border)", borderRadius: 8 }} /><Line type="monotone" dataKey="success_rate" stroke="var(--primary)" strokeWidth={2} dot={false} /></LineChart></ResponsiveContainer></div></StateView></CardContent></Card><Card><CardHeader><CardTitle>Cobertura</CardTitle></CardHeader><CardContent className="space-y-5"><Coverage label="Intentos" value={stats.attempt_coverage} /><Coverage label="Tokens" value={stats.token_coverage} /><Coverage label="Coste" value={stats.cost_coverage} /><Coverage label="RSS" value={stats.rss_coverage} /></CardContent></Card></div></StateView></div>;
}
function asRecord(value: unknown): Record<string, any> { return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, any> : {}; }
function Coverage({ label, value }: { label: string; value: unknown }) { const numeric = typeof value === "number" && Number.isFinite(value) ? Math.max(0, Math.min(100, value)) : null; return <div><div className="mb-2 flex justify-between text-xs"><span>{label}</span><span className="text-muted-foreground">{numeric === null ? "No medido" : `${numeric}%`}</span></div><div className="h-2 overflow-hidden rounded-full bg-muted"><div className="h-full rounded-full bg-primary transition-all" style={{ width: numeric === null ? "0%" : `${numeric}%` }} /></div></div>; }
