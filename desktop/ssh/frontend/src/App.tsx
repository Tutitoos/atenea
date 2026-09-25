import { useEffect, useMemo, useState } from 'react'

type ControllerView = { state: string; detail: string }
type Host = { alias: string; address: string; platform: string; agent: string; tone: 'ready' | 'pending' | 'error' }
type ProbeOutcome = 'resolved' | 'rejected' | 'timeout' | 'unavailable'
type ProbeView = { detail: string; tone: 'pending' | 'safe' | 'warning' }

declare global {
  interface Window {
    go?: { main?: { App?: { ControllerStatus: () => Promise<ControllerView> } } }
    runtime?: { ScreenGetAll?: () => Promise<unknown>; ClipboardGetText?: () => Promise<unknown> }
    WailsInvoke?: (message: string) => void
  }
}

const hosts: Host[] = [
  { alias: 'equipo-diseno', address: '192.0.2.12', platform: 'macOS', agent: 'Agente disponible', tone: 'ready' },
  { alias: 'servidor-pruebas', address: '192.0.2.24', platform: 'Linux', agent: 'Sin instalar', tone: 'pending' },
  { alias: 'estacion-taller', address: '192.0.2.36', platform: 'Windows', agent: 'Conexión pendiente', tone: 'error' },
]

const activity = [
  { time: '10:42', title: 'Sesión de ejemplo iniciada', detail: 'equipo-diseno · Vista previa' },
  { time: 'Ayer', title: 'Estado del agente consultado', detail: 'servidor-pruebas · Vista previa' },
]

function App() {
  const [query, setQuery] = useState('')
  const [selected, setSelected] = useState(hosts[0].alias)
  const [visibility, setVisibility] = useState<'visible' | 'oculto'>('visible')
  const [controller, setController] = useState<ControllerView>({ state: 'checking', detail: 'Comprobando controlador…' })
  const [bridgeProbe, setBridgeProbe] = useState<ProbeView>({ detail: 'Comprobando puente…', tone: 'pending' })
  const [navigationProbe, setNavigationProbe] = useState('Preparando intento de navegación…')
  const [theme, setTheme] = useState<'light' | 'dark'>(() => window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light')
  const filtered = useMemo(() => hosts.filter(host => `${host.alias} ${host.address} ${host.platform}`.toLowerCase().includes(query.toLowerCase())), [query])
  const host = hosts.find(item => item.alias === selected) ?? hosts[0]

  useEffect(() => {
    document.documentElement.dataset.theme = theme
  }, [theme])

  useEffect(() => {
    const status = window.go?.main?.App?.ControllerStatus
    if (!status) { setController({ state: 'preview', detail: 'Vista previa en navegador' }); return }
    void status().then(setController).catch(() => setController({ state: 'error', detail: 'No se pudo consultar el controlador' }))
  }, [])

  useEffect(() => {
    if (import.meta.env.VITE_ATENEA_BRIDGE_PROBE !== '1') return
    // A direct framework command must not change the native window title.
    window.WailsInvoke?.('WTAtenea SSH probe escaped')
    if (!window.runtime) { setBridgeProbe({ detail: 'Runtime no disponible', tone: 'warning' }); return }
    const check = async (call: (() => Promise<unknown>) | undefined): Promise<ProbeOutcome> => {
      if (!call) return 'unavailable'
      // Never inspect or render a framework result, which could contain local data.
      try {
        return await Promise.race<ProbeOutcome>([
          Promise.resolve().then(call).then<ProbeOutcome, ProbeOutcome>(() => 'resolved', () => 'rejected'),
          new Promise<ProbeOutcome>(resolve => window.setTimeout(() => resolve('timeout'), 1500)),
        ])
      } catch { return 'rejected' }
    }
    void Promise.all([
      check(window.runtime?.ScreenGetAll),
      check(window.runtime?.ClipboardGetText),
    ]).then(([screen, clipboard]) => {
      const outcomes: Record<ProbeOutcome, string> = {
        resolved: 'respondió', rejected: 'rechazada', timeout: 'sin respuesta', unavailable: 'no disponible',
      }
      setBridgeProbe({
        detail: `Pantalla: ${outcomes[screen]} · Portapapeles: ${outcomes[clipboard]}`,
        tone: screen === 'rejected' && clipboard === 'rejected' ? 'safe' : 'warning',
      })
    })
  }, [])

  useEffect(() => {
    if (import.meta.env.VITE_ATENEA_NAVIGATION_PROBE !== '1') return
    const timer = window.setTimeout(() => {
      try {
        window.location.assign('http://127.0.0.1:9/atenea-navigation-probe')
        window.setTimeout(() => setNavigationProbe('La página empaquetada sigue visible tras el intento'), 1500)
      } catch {
        setNavigationProbe('El navegador rechazó el intento de navegación')
      }
    }, 1000)
    return () => window.clearTimeout(timer)
  }, [])

  return <main className="shell">
    <aside className="sidebar">
      <div className="brand"><span className="brand-mark">✳</span><span><strong>Atenea SSH</strong><small>Espacio de trabajo</small></span></div>
      <div className="sidebar-label">NAVEGACIÓN</div>
      <button className="nav-item active" type="button" aria-current="page" title="Dispositivos"><span aria-hidden="true">▦</span> Dispositivos <span className="nav-count">{hosts.length}</span></button>
      <button className="nav-item" type="button" title="Historial" onClick={() => document.getElementById('history')?.scrollIntoView({ behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' })}><span aria-hidden="true">◷</span> Historial</button>
      <div className="sidebar-bottom"><span className="avatar">A</span><div><strong>Mi espacio</strong><small>Interfaz de ejemplo</small></div></div>
    </aside>
    <div className="workspace">
      <header className="topbar"><div><span className="breadcrumb">Espacio de trabajo</span><span className="slash">/</span><strong>Dispositivos</strong></div><button className="theme-button" type="button" onClick={() => setTheme(theme === 'light' ? 'dark' : 'light')} aria-label={theme === 'light' ? 'Activar modo oscuro' : 'Activar modo claro'}>{theme === 'light' ? '◐' : '☀'}</button></header>
      <div className="content">
        {import.meta.env.VITE_ATENEA_BRIDGE_PROBE === '1' && <div className="bridge-probe" data-tone={bridgeProbe.tone} role="status">{bridgeProbe.detail}</div>}
        {import.meta.env.VITE_ATENEA_NAVIGATION_PROBE === '1' && <div className="bridge-probe" role="status">{navigationProbe}</div>}
        <div className="heading"><div><div className="eyebrow">CONEXIONES SSH</div><h1>Tus dispositivos</h1><p>Consulta el estado de tus equipos y elige dónde trabajar.</p></div><span className="fixture-badge">DATOS DE EJEMPLO</span></div>
        <div className="controller-state" role="status"><span className={`state-dot ${controller.state}`}></span><span><strong>Controlador local</strong> · {controller.detail}</span></div>
        <div className="columns"><section className="list-panel" aria-label="Dispositivos de ejemplo"><div className="panel-heading"><h2>Dispositivos</h2><span>{filtered.length} de {hosts.length}</span></div><label className="search"><span aria-hidden="true">⌕</span><input value={query} onChange={event => setQuery(event.target.value)} placeholder="Buscar dispositivo…" aria-label="Buscar dispositivo" /></label><div className="host-list">{filtered.map(item => <button type="button" key={item.alias} className={`host-row ${selected === item.alias ? 'selected' : ''}`} aria-pressed={selected === item.alias} onClick={() => setSelected(item.alias)}><span className="device-icon">▣</span><span className="host-copy"><strong>{item.alias}</strong><small>{item.address} · {item.platform}</small></span><span className={`status-dot ${item.tone}`} role="img" aria-label={item.agent} /></button>)}{filtered.length === 0 && <p className="empty">No hay dispositivos con ese nombre.</p>}</div></section>
        <section className="detail-panel" aria-label="Detalle del dispositivo"><div className="detail-top"><span className="large-device">▣</span><span className={`status-pill ${host.tone}`}>{host.agent}</span></div><h2>{host.alias}</h2><p className="device-address">{host.address} · {host.platform}</p><div className="divider"/><div className="meta"><div><span>Alias SSH</span><strong>{host.alias}</strong></div><div><span>Estado del agente</span><strong>{host.agent}</strong></div><div><span>Conversación</span><strong>{visibility === 'visible' ? 'Visible' : 'Oculta'}</strong></div></div><div className="visibility"><div><strong>Visibilidad del chat</strong><small>Elige cómo aparecerá la conversación en este equipo.</small></div><div className="segmented" role="group" aria-label="Visibilidad del chat"><button type="button" aria-pressed={visibility === 'visible'} onClick={() => setVisibility('visible')}>Visible</button><button type="button" aria-pressed={visibility === 'oculto'} onClick={() => setVisibility('oculto')}>Oculto</button></div></div><button className="connect" type="button" disabled>Conectar próximamente <span>→</span></button><p className="fixture-note">Esta vista no abre conexiones SSH ni guarda preferencias.</p></section></div>
        <section id="history" className="history"><div className="history-heading"><div><div className="eyebrow">ACTIVIDAD</div><h2>Historial reciente</h2></div><span>Vista previa</span></div>{activity.map(item => <div className="activity-row" key={item.title}><span className="activity-icon">◷</span><div><strong>{item.title}</strong><small>{item.detail}</small></div><time>{item.time}</time></div>)}</section>
      </div>
    </div>
  </main>
}

export default App
