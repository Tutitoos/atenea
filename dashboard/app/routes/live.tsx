import { useEffect, useMemo } from "react";
import { Link, useSearchParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { ReactFlow, Background, Controls, Handle, Position, type Edge, type Node } from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import dagre from "dagre";
import { Activity, ArrowRight, CircleDot, GitBranch, Wrench } from "lucide-react";
import { api } from "~/data/api";
import { keys } from "~/data/query";
import type { GraphEdge, GraphNode, Session } from "~/lib/types";
import { Badge } from "~/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "~/components/ui/card";
import { StateBadge, StateView } from "~/components/status";
import { PageHeader, formatDuration, relativeDate } from "~/components/page";

const nodes: Node[] = [
  { id: "request", position: { x: 40, y: 80 }, data: { label: "Solicitud" }, type: "flow" },
  { id: "run", position: { x: 280, y: 80 }, data: { label: "Run" }, type: "flow" },
  { id: "selector", position: { x: 520, y: 80 }, data: { label: "Selector / funnel" }, type: "flow" },
  { id: "tool", position: { x: 760, y: 20 }, data: { label: "Tool" }, type: "flow" },
  { id: "provider", position: { x: 760, y: 140 }, data: { label: "Provider" }, type: "flow" },
  { id: "close", position: { x: 1000, y: 80 }, data: { label: "Cierre" }, type: "flow" },
];
const edges: Edge[] = [{ id: "e1", source: "request", target: "run" }, { id: "e2", source: "run", target: "selector" }, { id: "e3", source: "selector", target: "tool" }, { id: "e4", source: "selector", target: "provider" }, { id: "e5", source: "tool", target: "close" }, { id: "e6", source: "provider", target: "close" }];
function FlowNode({ data }: { data: { label: string } }) { return <div className="relative rounded-lg border border-primary/50 bg-card px-4 py-3 text-xs font-medium shadow-sm"><Handle type="target" position={Position.Left} className="!size-2 !border-0 !bg-primary" /><span>{data.label}</span><Handle type="source" position={Position.Right} className="!size-2 !border-0 !bg-primary" /></div>; }
const nodeTypes = { flow: FlowNode };

export default function LiveRoute() {
  const [params, setParams] = useSearchParams();
  const selected = params.get("session") || "";
  const selectedNode = params.get("node") || "";
  const sessions = useQuery({ queryKey: keys.sessions({ range: "24h" }), queryFn: () => api.sessions({ range: "24h" }) });
  const detail = useQuery({ queryKey: keys.session(selected), queryFn: () => api.session(selected), enabled: Boolean(selected) });
  const items = sessions.data?.data?.items || [];
  useEffect(() => { const first = items[0]?.id; if (!selected && first) setParams((old) => { old.set("session", first); return old; }); }, [items, selected, setParams]);
  const activeItems = useMemo(() => items.filter((item) => item.active || item.state === "running"), [items]);
  const flow = useMemo(() => buildFlow(detail.data?.data?.graph?.nodes, detail.data?.data?.graph?.edges), [detail.data]);
  const currentNode = detail.data?.data?.graph?.nodes?.find((node) => node.id === selectedNode);
  return <div><PageHeader eyebrow="Execution Graph" title="Live" description="Flujo de sesiones, runs, selector, tools y providers en tiempo real."><div className="flex items-center gap-2"><Activity className="size-4 text-[color:var(--status-success)]" /><span className="text-xs text-muted-foreground">Actualización SSE</span></div></PageHeader><div className="grid gap-4 xl:grid-cols-[280px_minmax(0,1fr)_300px]"><Card><CardHeader><CardTitle>Sesiones activas</CardTitle></CardHeader><CardContent className="space-y-2 pt-0"><StateView loading={sessions.isLoading} error={sessions.isError} empty={!activeItems.length}>{activeItems.map((session) => <SessionItem key={session.id} session={session} selected={selected === session.id} onSelect={() => setParams((old) => { old.set("session", session.id); old.delete("node"); return old; })} />)}</StateView><Link to="/sessions" className="mt-3 inline-flex items-center gap-2 text-xs text-primary hover:underline">Ver historial <ArrowRight className="size-3" /></Link></CardContent></Card><Card className="min-h-[500px] overflow-hidden"><CardHeader className="border-b"><CardTitle>Grafo de ejecución</CardTitle><p className="mt-1 text-xs text-muted-foreground">{detail.isFetching ? "Reconstruyendo desde Atenea…" : "Nodos estables · solo lectura"}</p></CardHeader><div className="h-[420px] w-full bg-background"><ReactFlow nodes={flow.nodes} edges={flow.edges} nodeTypes={nodeTypes} fitView proOptions={{ hideAttribution: true }} nodesDraggable={false} nodesConnectable={false} panOnDrag zoomOnScroll onNodeClick={(_, node) => setParams((old) => { old.set("node", node.id); return old; })}><Background color="var(--border)" gap={24} /><Controls showInteractive={false} /></ReactFlow></div><div className="border-t p-4 text-xs text-muted-foreground">Vista accesible: {flow.nodes.map((node, index) => <span key={node.id}>{index > 0 && <ArrowRight className="mx-1 inline size-3" />}<button className="underline-offset-2 hover:underline" onClick={() => setParams((old) => { old.set("node", node.id); return old; })}>{String(node.data.label)}</button></span>)}</div></Card><Inspector session={items.find((item) => item.id === selected)} node={currentNode} /></div></div>;
}
function SessionItem({ session, selected, onSelect }: { session: Session; selected: boolean; onSelect: () => void }) { return <button className={`w-full rounded-lg border p-3 text-left transition-colors ${selected ? "border-primary bg-accent" : "hover:bg-accent/50"}`} onClick={onSelect}><div className="flex items-center justify-between gap-2"><span className="truncate text-sm font-medium">{session.name || "Sesión sin nombre"}</span><CircleDot className="size-3 shrink-0 text-[color:var(--status-success)]" /></div><p className="mt-1 truncate text-xs text-muted-foreground">{session.primary_project || "Unknown"}</p><div className="mt-2 flex items-center justify-between"><StateBadge value={session.state || "active"} /><span className="text-[10px] text-muted-foreground">{relativeDate(session.updated_at)}</span></div></button>; }
function Inspector({ session, node }: { session?: Session; node?: GraphNode }) { return <Card><CardHeader><CardTitle>Inspector</CardTitle><p className="mt-1 text-xs text-muted-foreground">Contexto de la sesión seleccionada</p></CardHeader><CardContent className="space-y-4 pt-0">{session ? <><div><p className="text-sm font-medium">{session.name || "Sesión sin nombre"}</p><p className="mt-1 font-mono text-[10px] text-muted-foreground">{session.id}</p></div><StateBadge value={session.state || "active"} />{node && <div className="rounded-lg border bg-background p-3"><p className="text-xs font-medium">Nodo seleccionado</p><p className="mt-1 text-sm">{node.label || node.kind || node.id}</p><p className="mt-1 text-xs text-muted-foreground">{node.kind || "actividad"} · {node.state || "unknown"}</p></div>}<div className="space-y-3 text-xs"><Info icon={GitBranch} label="Proyecto" value={session.primary_project || "Unknown"} /><Info icon={Wrench} label="Runs" value={typeof session.stats?.runs === "number" ? String(session.stats.runs) : "No medido"} /><Info icon={Activity} label="Duración" value={formatDuration(session.stats?.duration_ms)} /><Info icon={CircleDot} label="Actividad" value={relativeDate(session.updated_at)} /></div><Link to={`/sessions/${encodeURIComponent(session.id)}`} className="inline-flex text-xs text-primary hover:underline">Abrir dossier <ArrowRight className="ml-1 size-3" /></Link></> : <p className="text-sm text-muted-foreground">Selecciona una sesión activa para inspeccionarla.</p>}</CardContent></Card>; }
function Info({ icon: Icon, label, value }: { icon: typeof Activity; label: string; value: string }) { return <div className="flex items-center justify-between gap-3"><span className="flex items-center gap-2 text-muted-foreground"><Icon className="size-3" />{label}</span><span className="text-right">{value}</span></div>; }

function buildFlow(graphNodes?: GraphNode[], graphEdges?: GraphEdge[]) {
	if (!graphNodes?.length) return { nodes, edges };
	const flowNodes: Node[] = graphNodes.map((node, index) => ({ id: node.id, position: { x: (index % 3) * 230 + 30, y: Math.floor(index / 3) * 100 + 30 }, data: { label: node.label || node.kind || node.id }, type: "flow" }));
	const flowEdges: Edge[] = (graphEdges || []).map((edge, index) => ({ id: `edge:${index}:${edge.from || edge.source || ""}:${edge.to || edge.target || ""}`, source: edge.from || edge.source || "", target: edge.to || edge.target || "" }));
	return { nodes: layoutFlow(flowNodes, flowEdges), edges: flowEdges };
}

function layoutFlow(flowNodes: Node[], flowEdges: Edge[]) {
	const graph = new dagre.graphlib.Graph().setDefaultEdgeLabel(() => ({}));
	graph.setGraph({ rankdir: "TB", ranksep: 64, nodesep: 28, marginx: 24, marginy: 24 });
	for (const node of flowNodes) graph.setNode(node.id, { width: 188, height: 64 });
	for (const edge of flowEdges) graph.setEdge(edge.source, edge.target);
	dagre.layout(graph);
	return flowNodes.map((node) => {
		const point = graph.node(node.id);
		return point ? { ...node, position: { x: point.x - 94, y: point.y - 32 } } : node;
	});
}
