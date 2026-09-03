import { Link, useParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { ArrowLeft, Bot, CheckCircle2, CircleDot, GitBranch } from "lucide-react";
import { api } from "~/data/api";
import { keys } from "~/data/query";
import { Card, CardContent, CardHeader, CardTitle } from "~/components/ui/card";
import { StateBadge, StateView } from "~/components/status";
import { PageHeader, formatDuration, relativeDate } from "~/components/page";

export default function RunDetailRoute() {
  const { runId = "" } = useParams();
  const result = useQuery({ queryKey: keys.run(runId), queryFn: () => api.run(runId), enabled: Boolean(runId) });
  const run = result.data?.data;
  return <div><Link to="/runs" className="mb-5 inline-flex items-center gap-2 text-xs text-muted-foreground hover:text-foreground"><ArrowLeft className="size-3" /> Volver a Runs</Link><PageHeader eyebrow="Run Ledger · detalle" title={run?.task || "Objetivo redactado"} description={`${run?.project || "Proyecto desconocido"} · ${run?.id || runId}`}><StateBadge value={run?.state} /></PageHeader><StateView loading={result.isLoading} error={result.isError} empty={!run}><div className="grid gap-4 lg:grid-cols-[.8fr_1.2fr]"><Card><CardHeader><CardTitle>Resumen</CardTitle></CardHeader><CardContent className="space-y-4 text-sm"><Row icon={GitBranch} label="Sesión" value={run?.session || "Unknown"} /><Row icon={CircleDot} label="Inicio" value={relativeDate(run?.started_at)} /><Row icon={CheckCircle2} label="Duración" value={formatDuration(run?.duration_ms)} /><Row icon={Bot} label="Proyecto" value={run?.project || run?.repositories?.join(", ") || "Unknown"} /></CardContent></Card><Card><CardHeader><CardTitle>Pasos observados</CardTitle></CardHeader><CardContent className="pt-0"><div className="divide-y">{run?.steps?.length ? run.steps.map((step, index) => <div key={step.id || index} className="flex items-center justify-between gap-4 py-3"><div className="min-w-0"><p className="truncate text-sm font-medium">{step.capability || "Capability desconocida"}</p><p className="truncate text-xs text-muted-foreground">{step.implementation || step.tool || "Tool no medida"}{step.provider ? ` · ${step.provider}` : ""}</p></div><div className="flex shrink-0 items-center gap-3"><span className="text-xs text-muted-foreground">{formatDuration(step.duration_ms)}</span><StateBadge value={step.state} /></div></div>) : <p className="py-6 text-sm text-muted-foreground">No hay pasos detallados en este checkpoint.</p>}</div></CardContent></Card></div></StateView></div>;
}
function Row({ icon: Icon, label, value }: { icon: typeof Bot; label: string; value: string }) { return <div className="flex items-start gap-3"><Icon className="mt-0.5 size-4 text-primary" /><div><p className="text-xs text-muted-foreground">{label}</p><p className="mt-1 break-words">{value}</p></div></div>; }
