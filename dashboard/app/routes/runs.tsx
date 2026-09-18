import { useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router";
import { useInfiniteQuery } from "@tanstack/react-query";
import { Search } from "lucide-react";
import { api } from "~/data/api";
import { keys } from "~/data/query";
import { Button } from "~/components/ui/button";
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
  const result = useInfiniteQuery({ queryKey: keys.runs(filters), initialPageParam: "", queryFn: ({ pageParam }) => api.runs({ ...filters, cursor: pageParam || undefined }), getNextPageParam: (last) => last.next_cursor || undefined });
  const items = result.data?.pages.flatMap((page) => page.data?.items || []) || [];
  const total = result.data?.pages[0]?.data?.total;
  const updateRange = (next: string) => setParams((old) => { old.set("range", next); return old; });
  const updateSearch = (value: string) => {
    setQ(value);
    setParams((old) => {
      if (value.trim()) old.set("q", value);
      else old.delete("q");
      return old;
    }, { replace: true });
  };
  return <div><PageHeader eyebrow="Run Ledger" title="Runs" description="Historial paginado de ejecuciones, checkpoints y estados de cierre."><RangeControl value={range} onChange={updateRange} /></PageHeader><div className="mb-4 relative max-w-xl"><Search className="pointer-events-none absolute left-3 top-3.5 size-4 text-muted-foreground" /><Input className="pl-9" value={q} onChange={(event) => updateSearch(event.target.value)} placeholder="Buscar objetivo, proyecto o run" aria-label="Buscar runs" /></div><Card><CardContent className="p-0"><StateView loading={result.isLoading} error={result.isError && !result.data} empty={!items.length}><div className="divide-y">{items.map((run) => <RunRow key={run.id} run={run} />)}</div><p className="border-t px-4 py-3 text-xs text-muted-foreground">{formatNumber(items.length)} de {formatNumber(total ?? items.length, "No medido")} runs · más recientes primero</p>{result.hasNextPage && <div className="p-4 text-center"><Button variant="outline" disabled={result.isFetchingNextPage} onClick={() => void result.fetchNextPage()}>{result.isFetchingNextPage ? "Cargando…" : "Cargar más runs"}</Button></div>}{result.isFetchNextPageError && <p role="alert" className="px-4 pb-4 text-sm text-destructive">No se pudo cargar la siguiente página. Vuelve a intentarlo.</p>}</StateView></CardContent></Card></div>;
}
function RunRow({ run }: { run: Run }) { return <Link to={`/runs/${encodeURIComponent(run.id)}`} className="flex flex-col justify-between gap-3 p-4 hover:bg-accent/40 sm:flex-row sm:items-center"><div className="min-w-0"><p className="truncate text-sm font-medium">{run.task || "Objetivo redactado"}</p><p className="mt-1 flex flex-wrap gap-2 font-mono text-[10px] text-muted-foreground"><span>{run.id}</span>{run.project && <span>· {run.project}</span>}</p></div><div className="flex shrink-0 items-center gap-4 text-xs text-muted-foreground"><span>{relativeDate(run.started_at)}</span><span>{formatDuration(run.duration_ms)}</span><StateBadge value={run.state} /></div></Link>; }
