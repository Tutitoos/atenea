import { useQuery } from "@tanstack/react-query";
import { Boxes, CheckCircle2, CircleHelp, GitBranch } from "lucide-react";
import { api } from "~/data/api";
import { keys } from "~/data/query";
import { Card, CardContent, CardHeader, CardTitle } from "~/components/ui/card";
import { PageHeader } from "~/components/page";
import { StateView } from "~/components/status";

export default function CatalogRoute() {
  const result = useQuery({ queryKey: keys.catalog, queryFn: () => api.catalog() });
  const items = asItems(result.data?.data);
  return <div><PageHeader eyebrow="Availability Map" title="Catalog" description="Capabilities, implementaciones y redundancia disponible para routing seguro." /><StateView loading={result.isLoading} error={result.isError} empty={!items.length}><div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">{items.map((item, index) => <Card key={String(item.id || item.name || index)}><CardHeader><CardTitle className="flex items-center gap-2"><Boxes className="size-4 text-primary" />{String(item.name || item.id || `Capability ${index + 1}`)}</CardTitle></CardHeader><CardContent className="space-y-3 pt-0 text-xs"><Line icon={GitBranch} label="Implementaciones" value={String(item.implementations || item.count || "No medido")} /><Line icon={CheckCircle2} label="Disponibilidad" value={String(item.status || "Unknown")} /><Line icon={CircleHelp} label="Cobertura" value={String(item.coverage || "No medido")} /></CardContent></Card>)}</div></StateView></div>;
}
function Line({ icon: Icon, label, value }: { icon: typeof Boxes; label: string; value: string }) { return <div className="flex items-center justify-between gap-3"><span className="flex items-center gap-2 text-muted-foreground"><Icon className="size-3" />{label}</span><span>{value}</span></div>; }
function asItems(value: unknown): Array<Record<string, unknown>> { if (Array.isArray(value)) return value.filter((item): item is Record<string, unknown> => Boolean(item && typeof item === "object")); if (value && typeof value === "object") { const record = value as Record<string, unknown>; return asItems(record.items || record.capabilities || record.catalog); } return []; }
