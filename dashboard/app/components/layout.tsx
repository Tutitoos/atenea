import { useEffect, useMemo, useState } from "react";
import type { FormEvent } from "react";
import { NavLink, Outlet, useLocation } from "react-router";
import { Activity, AlertTriangle, BarChart3, Boxes, ChartNoAxesCombined, ChevronLeft, CircleDot, Cpu, LayoutDashboard, Menu, Moon, Network, PanelLeft, Search, Settings2, Sun, X } from "lucide-react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Button } from "./ui/button";
import { Separator } from "./ui/separator";
import { LiveDot } from "./status";
import { ThemeProvider, useTheme, type Theme } from "~/data/theme";
import { useRealtime } from "~/data/query";
import { api, UnauthorizedError } from "~/data/api";

const queryClient = new QueryClient({ defaultOptions: { queries: { staleTime: 5_000, retry: 2, refetchOnWindowFocus: false } } });
const nav = [
  { to: "/", label: "Overview", icon: LayoutDashboard, end: true },
  { to: "/live", label: "Live", icon: Activity },
  { to: "/sessions", label: "Sessions", icon: CircleDot },
  { to: "/runs", label: "Runs", icon: ChartNoAxesCombined },
  { to: "/metrics", label: "Metrics", icon: BarChart3 },
  { to: "/infrastructure", label: "Infrastructure", icon: Network },
  { to: "/incidents", label: "Incidents", icon: AlertTriangle },
  { to: "/catalog", label: "Catalog", icon: Boxes },
];

function ConnectionStatus() {
  const [connected, setConnected] = useState(true);
  useEffect(() => { const handler = (event: Event) => setConnected(Boolean((event as CustomEvent<{ connected: boolean }>).detail.connected)); window.addEventListener("atenea:connection", handler); return () => window.removeEventListener("atenea:connection", handler); }, []);
  return <div className="hidden items-center gap-2 text-xs text-muted-foreground sm:flex"><LiveDot state={connected ? "green" : "amber"} /><span>{connected ? "Conexión fresca" : "Reconectando"}</span></div>;
}

function ThemeControl() {
  const { theme, setTheme } = useTheme();
  const next: Record<Theme, Theme> = { system: "dark", dark: "light", light: "system" };
  const Icon = theme === "light" ? Sun : theme === "dark" ? Moon : Settings2;
  return <Button variant="ghost" size="icon" aria-label={`Tema: ${theme}. Cambiar`} onClick={() => setTheme(next[theme])}><Icon className="size-4" /></Button>;
}

function AuthGate() {
  const [token, setToken] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true); setError("");
    try { await api.login(token); window.dispatchEvent(new Event("atenea:auth-resolved")); setToken(""); }
    catch (reason) { setError(reason instanceof UnauthorizedError ? "Token no válido" : "No se pudo iniciar sesión"); }
    finally { setBusy(false); }
  };
  return <div className="grid min-h-[60vh] place-items-center"><form onSubmit={submit} className="w-full max-w-md rounded-xl border bg-card p-6 shadow-sm"><div className="mb-5 grid size-10 place-items-center rounded-xl bg-primary font-bold text-primary-foreground">A</div><h2 className="text-xl font-semibold">Panel protegido</h2><p className="mt-2 text-sm text-muted-foreground">Introduce el token del listener LAN. No se guardará en el navegador.</p><label className="mt-5 block text-xs font-medium" htmlFor="dashboard-token">Token de acceso</label><input id="dashboard-token" className="mt-2 h-11 w-full rounded-md border bg-background px-3 text-sm" type="password" autoComplete="current-password" value={token} onChange={(event) => setToken(event.target.value)} /><Button className="mt-4 w-full" type="submit" disabled={busy || !token}>{busy ? "Comprobando…" : "Entrar"}</Button>{error && <p className="mt-3 text-xs text-[color:var(--status-danger)]" role="alert">{error}</p>}</form></div>;
}

function Sidebar({ mobile = false, onClose }: { mobile?: boolean; onClose?: () => void }) {
  return <aside className={mobile ? "fixed inset-y-0 left-0 z-50 flex w-72 flex-col border-r bg-card p-4 shadow-xl lg:hidden" : "material hidden w-64 shrink-0 flex-col border-r bg-card/80 p-4 lg:flex"} aria-label="Navegación principal">
    <div className="flex items-center justify-between px-2 pb-5"><NavLink to="/" onClick={onClose} className="flex items-center gap-3"><span className="grid size-9 place-items-center rounded-xl bg-primary text-lg font-bold text-primary-foreground">A</span><span><strong className="block text-sm">Atenea</strong><small className="block text-[10px] text-muted-foreground">Observabilidad</small></span></NavLink>{mobile && <Button variant="ghost" size="icon" onClick={onClose} aria-label="Cerrar menú"><X className="size-4" /></Button>}</div>
    <Separator />
    <nav className="mt-5 flex-1 space-y-1">{nav.map(({ to, label, icon: Icon, end }) => <NavLink key={to} end={end} to={to} onClick={onClose} className={({ isActive }) => `flex min-h-11 items-center gap-3 rounded-lg px-3 text-sm transition-colors ${isActive ? "bg-accent font-medium text-accent-foreground" : "text-muted-foreground hover:bg-accent/60 hover:text-foreground"}`}><Icon className="size-4" /><span>{label}</span>{label === "Live" && <LiveDot />}</NavLink>)}</nav>
    <div className="rounded-lg border bg-background p-3 text-xs text-muted-foreground"><p className="font-medium text-foreground">Solo lectura</p><p className="mt-1">Sin acciones de control.</p></div>
  </aside>;
}

function Shell() {
  const [menuOpen, setMenuOpen] = useState(false);
  const [authRequired, setAuthRequired] = useState(false);
  const location = useLocation();
  const title = useMemo(() => nav.find((item) => item.end ? location.pathname === item.to : location.pathname.startsWith(item.to))?.label || "Observabilidad", [location.pathname]);
  useRealtime();
  useEffect(() => { const required = () => setAuthRequired(true); const resolved = () => { setAuthRequired(false); void queryClient.invalidateQueries(); }; window.addEventListener("atenea:auth-required", required); window.addEventListener("atenea:auth-resolved", resolved); return () => { window.removeEventListener("atenea:auth-required", required); window.removeEventListener("atenea:auth-resolved", resolved); }; }, []);
  if (authRequired) return <><header className="material flex min-h-16 items-center border-b bg-background/80 px-4"><span className="grid size-8 place-items-center rounded-lg bg-primary text-sm font-bold text-primary-foreground">A</span><span className="ml-3 text-sm font-semibold">Atenea</span></header><main className="min-w-0 flex-1 px-4 py-6 sm:px-6 lg:px-8"><AuthGate /></main></>;
  return <div className="flex min-h-svh bg-background text-foreground"><Sidebar mobile={menuOpen} onClose={() => setMenuOpen(false)} /><Sidebar /><div className="flex min-w-0 flex-1 flex-col"><header className="material sticky top-0 z-30 flex min-h-16 items-center justify-between border-b bg-background/80 px-4 backdrop-blur-xl sm:px-6"><div className="flex items-center gap-3"><Button className="lg:hidden" variant="ghost" size="icon" onClick={() => setMenuOpen(true)} aria-label="Abrir menú"><Menu className="size-5" /></Button><div><p className="text-[10px] font-semibold uppercase tracking-[0.2em] text-primary">Atenea</p><h1 className="text-sm font-semibold sm:text-base">{title}</h1></div></div><div className="flex items-center gap-2"><ConnectionStatus /><ThemeControl /><Button variant="ghost" size="icon" aria-label="Buscar"><Search className="size-4" /></Button></div></header><main className="min-w-0 flex-1 px-4 py-6 sm:px-6 lg:px-8"><Outlet /></main><nav className="material sticky bottom-0 z-30 grid grid-cols-4 border-t bg-background/90 p-1 backdrop-blur-xl lg:hidden">{nav.slice(0, 3).map(({ to, label, icon: Icon, end }) => <NavLink key={to} end={end} to={to} className={({ isActive }) => `grid min-h-12 place-items-center gap-0.5 rounded-md text-[10px] ${isActive ? "bg-accent text-primary" : "text-muted-foreground"}`}><Icon className="size-4" /><span>{label}</span></NavLink>)}<button className="grid min-h-12 place-items-center gap-0.5 rounded-md text-[10px] text-muted-foreground" onClick={() => setMenuOpen(true)}><PanelLeft className="size-4" /><span>Más</span></button></nav></div></div>;
}

export default function AppLayout() { return <ThemeProvider><QueryClientProvider client={queryClient}><Shell /></QueryClientProvider></ThemeProvider>; }
