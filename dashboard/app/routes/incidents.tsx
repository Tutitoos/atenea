import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, CircleHelp, Clock3 } from "lucide-react";
import { api } from "~/data/api";
import { keys } from "~/data/query";
import { Card, CardContent, CardHeader, CardTitle } from "~/components/ui/card";
import { PageHeader, relativeDate } from "~/components/page";
import { StateView } from "~/components/status";

export default function IncidentsRoute() {
  const result = useQuery({ queryKey: keys.incidents, queryFn: () => api.incidents() });
  const items = asItems(result.data?.data);
  return <div><PageHeader eyebrow="Incident Timeline" title="Incidents" description="Cronología redactada y de solo lectura. Las causas no confirmadas se distinguen de las correlaciones." /><StateView loading={result.isLoading} error={result.isError} empty={!items.length}><Card><CardHeader><CardTitle>Actividad e impacto</CardTitle></CardHeader><CardContent className="pt-0"><div className="divide-y">{items.map((item, index) => <div key={String(item.id || index)} className="flex gap-4 py-4"><span className="mt-0.5 grid size-8 shrink-0 place-items-center rounded-full bg-[color:var(--status-warning)]/15 text-[color:var(--status-warning)]"><AlertTriangle className="size-4" /></span><div className="min-w-0 flex-1"><div className="flex flex-wrap items-center justify-between gap-2"><p className="text-sm font-medium">{String(item.title || item.name || "Incidente observado")}</p><span className="flex items-center gap-1 text-xs text-muted-foreground"><Clock3 className="size-3" />{relativeDate(typeof item.ts === "string" ? item.ts : undefined)}</span></div><p className="mt-1 text-xs text-muted-foreground">{String(item.summary || item.status || "Evidencia parcial")}</p><p className="mt-2 text-[11px] text-muted-foreground"><CircleHelp className="mr-1 inline size-3" />Causa no confirmada</p></div></div>)}</div></CardContent></Card></StateView></div>;
}
function asItems(value: unknown): Array<Record<string, unknown>> { if (Array.isArray(value)) return value.filter((item): item is Record<string, unknown> => Boolean(item && typeof item === "object")); if (value && typeof value === "object") { const record = value as Record<string, unknown>; return asItems(record.items || record.incidents); } return []; }
