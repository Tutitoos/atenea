import { useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { Search } from "lucide-react";
import { api } from "~/data/api";
import { keys } from "~/data/query";
import { Card, CardContent } from "~/components/ui/card";
import { Input } from "~/components/ui/input";
import { StateBadge, StateView } from "~/components/status";
import { PageHeader, RangeControl, formatDuration, relativeDate, formatNumber } from "~/components/page";
import type { Run } from "~/lib/types";

export default function RunsRoute() {
  const [params, setParams] = useSearchParams();
  const range = params.get("range") || "24h";
  const [q, setQ] = useState(params.get("q") || "");
  const filters = useMemo(() => ({ range, q: q || undefined, state: params.get("state") || undefined, project: params.get("project") || undefined }), [range, q, params]);
  const result = useQuery({ queryKey: keys.runs(filters), queryFn: () => api.runs(filters) });
  const items = result.data?.data?.items || [];
  const updateRange = (next: string) => setParams((old) => { old.set("range", next); return old; });
  const updateSearch = (value: string) => {
    setQ(value);
    setParams((old) => {
      if (value.trim()) old.set("q", value);
      else old.delete("q");
      return old;
    }, { replace: true });
  };
  return <div><PageHeader eyebrow="Run Ledger" title="Runs" description="Historial paginado de ejecuciones, checkpoints y estados de cierre."><RangeControl value={range} onChange={updateRange} /></PageHeader><div className="mb-4 relative max-w-xl"><Search className="pointer-events-none absolute left-3 top-3.5 size-4 text-muted-foreground" /><Input className="pl-9" value={q} onChange={(event) => updateSearch(event.target.value)} placeholder="Buscar objetivo, proyecto o run" aria-label="Buscar runs" /></div><Card><CardContent className="p-0"><StateView loading={result.isLoading} error={result.isError} empty={!items.length}><div className="divide-y">{items.map((run) => <RunRow key={run.id} run={run} />)}</div><p className="border-t px-4 py-3 text-xs text-muted-foreground">{formatNumber(result.data?.data?.total ?? items.length, "No medido")} runs · más recientes primero</p></StateView></CardContent></Card></div>;
}
function RunRow({ run }: { run: Run }) { return <Link to={`/runs/${encodeURIComponent(run.id)}`} className="flex flex-col justify-between gap-3 p-4 hover:bg-accent/40 sm:flex-row sm:items-center"><div className="min-w-0"><p className="truncate text-sm font-medium">{run.task || "Objetivo redactado"}</p><p className="mt-1 flex flex-wrap gap-2 font-mono text-[10px] text-muted-foreground"><span>{run.id}</span>{run.project && <span>· {run.project}</span>}</p></div><div className="flex shrink-0 items-center gap-4 text-xs text-muted-foreground"><span>{relativeDate(run.started_at)}</span><span>{formatDuration(run.duration_ms)}</span><StateBadge value={run.state} /></div></Link>; }
