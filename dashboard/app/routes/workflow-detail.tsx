import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { ArrowLeft, Bot, CheckCircle2, Clock3, Database, Wrench } from "lucide-react";
import { Link, useParams } from "react-router";
import { Card, CardContent, CardHeader, CardTitle } from "~/components/ui/card";
import { PageHeader, formatDuration, formatNumber } from "~/components/page";
import { StateBadge, StateView } from "~/components/status";
import { api } from "~/data/api";
import { keys } from "~/data/query";

type MeasurementCounts = { measured?: number; estimated?: number; partial?: number; unknown?: number };

export function measuredMetricLabel(value: number, counts: MeasurementCounts, formatter: (value: number) => string) {
  const measured = counts.measured || 0;
  const incomplete = (counts.estimated || 0) + (counts.partial || 0) + (counts.unknown || 0);
  if (measured + incomplete === 0 || (value === 0 && incomplete > 0 && measured === 0)) return "Desconocido";
  const formatted = formatter(value);
  return incomplete > 0 ? `${formatted} · parcial` : formatted;
}

export default function WorkflowDetailRoute() {
  const { workflowId = "" } = useParams();
  // SSE refreshes workflows produced by this service immediately. A bounded
  // poll also observes CLI and other-process writers that share the durable
  // workflow store but cannot publish into this process's in-memory hub.
  const result = useQuery({ queryKey: keys.workflow(workflowId), queryFn: () => api.workflow(workflowId), enabled: Boolean(workflowId), refetchInterval: 2_000 });
  const workflow = result.data?.data;
  const points = workflow?.progress?.points?.filter((point) => !point.retired) || [];
  const accepted = points.filter((point) => point.state === "accepted").length;
  const percent = points.length ? Math.floor(accepted * 100 / points.length) : 0;
  const filled = points.length ? Math.floor(accepted * 20 / points.length) : 0;
  const telemetry = workflow?.telemetry || [];
  const tokens = telemetry.reduce((sum, row) => sum + (row.input_tokens || 0) + (row.output_tokens || 0) + (row.cache_read_tokens || 0) + (row.cache_write_tokens || 0), 0);
  const measuredReceipts = telemetry.reduce((sum, row) => sum + (row.measured || 0), 0);
  const incompleteReceipts = telemetry.reduce((sum, row) => sum + (row.estimated || 0) + (row.partial || 0) + (row.unknown || 0), 0);
  const tokenLabel = measuredMetricLabel(tokens, { measured: measuredReceipts, partial: incompleteReceipts }, formatNumber);
  const agentRuns = telemetry.reduce((sum, row) => sum + (row.agent_runs || 0), 0);
  const toolUses = telemetry.reduce((sum, row) => sum + (row.tool_uses || 0), 0);
  const activity = workflow?.activity || [];
  return <div><Link to="/runs" className="mb-5 inline-flex items-center gap-2 text-xs text-muted-foreground hover:text-foreground"><ArrowLeft className="size-3" /> Volver a Runs</Link><PageHeader eyebrow="Workflow · seguimiento" title={workflow?.task || "Workflow"} description={`${workflow?.repository || "Repositorio desconocido"} · ${workflow?.id || workflowId}`}><StateBadge value={workflow?.state} /></PageHeader><StateView loading={result.isLoading} error={result.isError} empty={!workflow}><div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4"><Metric label="Progreso" value={points.length ? `${percent}% · ${accepted}/${points.length}` : "Sin puntos"} icon={CheckCircle2} /><Metric label="Duración activa" value={formatDuration(workflow?.active_duration_ms)} icon={Clock3} /><Metric label="Tokens" value={tokenLabel} icon={Database} /><Metric label="Actividad" value={`${agentRuns} agentes · ${toolUses} herramientas`} icon={Bot} /></div><Card className="mt-4"><CardHeader><CardTitle>Plan persistido</CardTitle><p className="font-mono text-xs text-muted-foreground" aria-label={`${accepted} de ${points.length} puntos aceptados`}>{points.length ? `${"█".repeat(filled)}${"░".repeat(20 - filled)} ${percent}% · ${accepted}/${points.length}` : "sin puntos definidos"}</p></CardHeader><CardContent className="space-y-2 pt-0">{points.map((point) => <div key={point.id} className="flex items-start gap-3 rounded-lg border bg-background p-3"><span aria-hidden="true" className="font-mono">{point.state === "accepted" ? "[x]" : "[ ]"}</span><div className="min-w-0 flex-1"><p className="text-sm font-medium">{point.id}. {point.title}</p><StateBadge value={point.state} /></div></div>)}</CardContent></Card><div className="mt-4 grid gap-4 xl:grid-cols-2"><Disclosure title="Agentes, modelos y esfuerzo"><div className="divide-y">{activity.filter((item) => item.agent_run_id).map((item) => <div key={item.invocation_id} className="py-3 text-xs"><p className="font-medium">{item.point_id || "workflow-overhead"} · {item.tool || "agente"}</p><p className="mt-1 text-muted-foreground">Solicitado: {item.requested_model || "desconocido"} / {item.requested_reasoning_effort || "desconocido"}</p><p className="text-muted-foreground">Observado: {item.observed_model || "desconocido"} / {item.observed_reasoning_effort || "desconocido"}</p></div>)}</div></Disclosure><Disclosure title="Herramientas, tiempos y tokens"><div className="divide-y">{telemetry.map((row) => <TelemetryRow key={row.point_id} row={row} />)}</div></Disclosure><Disclosure title="Evidencia de aceptación"><div className="space-y-3">{points.map((point) => <div key={point.id}><p className="text-xs font-medium">{point.id}</p><p className="mt-1 break-all font-mono text-[10px] text-muted-foreground">{point.evidence?.join(" · ") || "Sin evidencia aceptada"}</p></div>)}</div></Disclosure><Disclosure title="Avisos publicados"><div className="space-y-3">{activity.map((item) => <blockquote key={item.invocation_id} className="border-l-2 pl-3 text-xs text-muted-foreground">{item.markdown || `${item.tool || "ATENEA"} · actividad`}</blockquote>)}{workflow?.activity_has_more && <p className="text-xs text-muted-foreground">Vista parcial; hay más actividad tras el cursor {workflow.activity_cursor}.</p>}</div></Disclosure></div></StateView></div>;
}

function TelemetryRow({ row }: { row: NonNullable<NonNullable<Awaited<ReturnType<typeof api.workflow>>["data"]>["telemetry"]>[number] }) {
  const counts = { measured: row.measured, estimated: row.estimated, partial: row.partial, unknown: row.unknown };
  const duration = (row.agent_duration || 0) / 1_000_000 + (row.tool_duration || 0) / 1_000_000;
  const tokens = (row.input_tokens || 0) + (row.output_tokens || 0) + (row.cache_read_tokens || 0) + (row.cache_write_tokens || 0);
  const durationCounts = { measured: duration > 0 ? 1 : 0 };
  return <div className="grid grid-cols-2 gap-2 py-3 text-xs"><strong>{row.point_id}</strong><span>{formatNumber((row.agent_runs || 0) + (row.tool_uses || 0))} usos</span><span>{measuredMetricLabel(duration, durationCounts, formatDuration)}</span><span>{measuredMetricLabel(tokens, counts, formatNumber)} tokens</span><span className="col-span-2 text-muted-foreground">Medidos {row.measured || 0} · estimados {row.estimated || 0} · parciales {row.partial || 0} · desconocidos {row.unknown || 0}</span></div>;
}

function Metric({ label, value, icon: Icon }: { label: string; value: string; icon: typeof Bot }) { return <Card><CardContent className="flex items-start gap-3 p-4"><Icon className="mt-0.5 size-4 text-primary" /><div><p className="text-xs text-muted-foreground">{label}</p><p className="mt-1 text-sm font-semibold">{value}</p></div></CardContent></Card>; }
function Disclosure({ title, children }: { title: string; children: ReactNode }) { return <details className="rounded-xl border bg-card"><summary className="flex min-h-12 cursor-pointer items-center gap-2 px-4 text-sm font-semibold"><Wrench className="size-4 text-primary" />{title}</summary><div className="border-t px-4 py-2">{children}</div></details>; }
