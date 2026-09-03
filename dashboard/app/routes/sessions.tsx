import { useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { Search, SlidersHorizontal } from "lucide-react";
import { api } from "~/data/api";
import { keys } from "~/data/query";
import type { Session } from "~/lib/types";
import { PageHeader, RangeControl, relativeDate, formatNumber } from "~/components/page";
import { Card, CardContent } from "~/components/ui/card";
import { Input } from "~/components/ui/input";
import { Button } from "~/components/ui/button";
import { StateBadge, StateView } from "~/components/status";

export default function SessionsRoute() {
  const [params, setParams] = useSearchParams();
  const range = params.get("range") || "24h";
  const [search, setSearch] = useState(params.get("q") || "");
  const filters = useMemo(() => ({ range, q: search || undefined, project: params.get("project") || undefined, origin: params.get("origin") || undefined, state: params.get("state") || undefined }), [range, search, params]);
  const result = useQuery({ queryKey: keys.sessions(filters), queryFn: () => api.sessions(filters) });
  const items = result.data?.data?.items || [];
  const updateRange = (next: string) => setParams((old) => { old.set("range", next); return old; });
  const updateSearch = (value: string) => {
    setSearch(value);
    setParams((old) => {
      if (value.trim()) old.set("q", value);
      else old.delete("q");
      return old;
    }, { replace: true });
  };
  return <div><PageHeader eyebrow="Session Table" title="Sessions" description="Conversaciones MCP agrupadas por nombre, proyecto y procedencia, con historial reconstruido desde Atenea."><RangeControl value={range} onChange={updateRange} /></PageHeader><div className="mb-4 flex flex-col gap-3 sm:flex-row"><div className="relative min-w-0 flex-1"><Search className="pointer-events-none absolute left-3 top-3.5 size-4 text-muted-foreground" /><Input className="pl-9" value={search} onChange={(event) => updateSearch(event.target.value)} placeholder="Buscar sesiones, proyectos o clientes" aria-label="Buscar sesiones" /></div><Button variant="outline" className="shrink-0"><SlidersHorizontal className="size-4" /> Filtros</Button></div><Card><CardContent className="p-0"><StateView loading={result.isLoading} error={result.isError} empty={!items.length}><div className="hidden overflow-x-auto md:block"><table className="w-full text-left text-sm"><thead className="border-b bg-muted/40 text-xs text-muted-foreground"><tr>{["Sesión", "Proyecto", "Origen", "Estado", "Última actividad", "Runs", "Éxito", "Cobertura"].map((header) => <th key={header} className="whitespace-nowrap px-4 py-3 font-medium">{header}</th>)}</tr></thead><tbody className="divide-y">{items.map((session) => <SessionTableRow key={session.id} session={session} />)}</tbody></table></div><div className="divide-y md:hidden">{items.map((session) => <SessionCard key={session.id} session={session} />)}</div><p className="border-t px-4 py-3 text-xs text-muted-foreground">{formatNumber(result.data?.data?.total ?? items.length, "No medido")} sesiones · orden más reciente</p></StateView></CardContent></Card></div>;
}

function SessionTableRow({ session }: { session: Session }) { const rate = session.stats?.success_rate ?? session.stats?.success; return <tr className="hover:bg-accent/40"><td className="max-w-[220px] px-4 py-3"><Link className="font-medium hover:text-primary hover:underline" to={`/sessions/${encodeURIComponent(session.id)}`}>{session.name || "Sesión sin nombre"}</Link><span className="mt-1 block font-mono text-[10px] text-muted-foreground">{session.id}</span></td><td className="px-4 py-3">{session.primary_project || "Unknown"}</td><td className="px-4 py-3 text-muted-foreground">{session.origin?.client || "Unknown"}</td><td className="px-4 py-3"><StateBadge value={session.state || (session.active ? "active" : "unknown")} /></td><td className="whitespace-nowrap px-4 py-3 text-muted-foreground">{relativeDate(session.updated_at)}</td><td className="px-4 py-3">{formatNumber(session.stats?.runs)}</td><td className="px-4 py-3">{typeof rate === "number" && Number.isFinite(rate) ? `${Math.round(rate)}%` : "No medido"}</td><td className="px-4 py-3">{typeof session.stats?.coverage === "number" ? `${session.stats.coverage}%` : "No medido"}</td></tr>; }
function SessionCard({ session }: { session: Session }) { return <Link to={`/sessions/${encodeURIComponent(session.id)}`} className="block p-4 hover:bg-accent/40"><div className="flex items-start justify-between gap-3"><div className="min-w-0"><p className="truncate font-medium">{session.name || "Sesión sin nombre"}</p><p className="mt-1 truncate font-mono text-[10px] text-muted-foreground">{session.id}</p></div><StateBadge value={session.state || (session.active ? "active" : "unknown")} /></div><div className="mt-3 grid grid-cols-2 gap-3 text-xs"><div><span className="text-muted-foreground">Proyecto</span><p className="mt-1">{session.primary_project || "Unknown"}</p></div><div><span className="text-muted-foreground">Origen</span><p className="mt-1">{session.origin?.client || "Unknown"}</p></div><div><span className="text-muted-foreground">Runs</span><p className="mt-1">{formatNumber(session.stats?.runs)}</p></div><div><span className="text-muted-foreground">Actividad</span><p className="mt-1">{relativeDate(session.updated_at)}</p></div></div></Link>; }
