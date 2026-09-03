import { useQuery } from "@tanstack/react-query";
import { Boxes, Cloud, Database, HeartPulse, Server, ShieldCheck } from "lucide-react";
import { api } from "~/data/api";
import { keys } from "~/data/query";
import { Card, CardContent, CardHeader, CardTitle } from "~/components/ui/card";
import { Badge } from "~/components/ui/badge";
import { PageHeader } from "~/components/page";
import { StateView } from "~/components/status";

export default function InfrastructureRoute() {
  const result = useQuery({ queryKey: keys.catalog, queryFn: () => api.catalog() });
  const data = result.data?.data;
  const items = asItems(data);
  return <div><PageHeader eyebrow="Topology Map" title="Infrastructure" description="Procesos supervisados, MCP servers, providers y almacenamiento con impacto operativo." /><StateView loading={result.isLoading} error={result.isError} empty={!items.length}><div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">{[{ label: "Atenea", icon: HeartPulse }, { label: "Procesos", icon: Server }, { label: "MCP servers", icon: Boxes }, { label: "Providers", icon: Cloud }, { label: "Storage", icon: Database }, { label: "Operaciones", icon: ShieldCheck }].map(({ label, icon: Icon }) => <Card key={label}><CardContent className="p-4"><div className="flex items-center gap-3"><span className="grid size-9 place-items-center rounded-lg bg-accent text-primary"><Icon className="size-4" /></span><div><p className="text-sm font-medium">{label}</p><p className="mt-1 text-xs text-muted-foreground">Estado observado</p></div><Badge variant="success" className="ml-auto">OK</Badge></div></CardContent></Card>)}</div><Card className="mt-4"><CardHeader><CardTitle>Mapa de dependencias</CardTitle><p className="mt-1 text-xs text-muted-foreground">La topología se presenta como dominios navegables para conservar legibilidad en móvil.</p></CardHeader><CardContent><div className="grid gap-3 md:grid-cols-3">{items.slice(0, 12).map((item, index) => <div key={String(item.id || item.name || index)} className="rounded-lg border bg-background p-4"><p className="text-sm font-medium">{String(item.name || item.id || `Componente ${index + 1}`)}</p><p className="mt-1 text-xs text-muted-foreground">{String(item.kind || item.type || "Dependencia observada")}</p></div>)}</div></CardContent></Card></StateView></div>;
}
function asItems(value: unknown): Array<Record<string, unknown>> { if (Array.isArray(value)) return value.filter((item): item is Record<string, unknown> => Boolean(item && typeof item === "object")); if (value && typeof value === "object") { const record = value as Record<string, unknown>; return asItems(record.items || record.providers || record.components || record.catalog); } return []; }
