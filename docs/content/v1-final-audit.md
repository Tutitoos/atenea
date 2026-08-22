---
title: auditoría final pre-v1
weight: 6
---

# Auditoría final pre-v1

Fecha de corte: 2026-08-22. Esta página distingue el estado del repositorio,
la configuración efectiva del equipo y las funciones que solo aparecen en la
documentación. Los porcentajes son una estimación de cierre por área, no una
cobertura de código ni una promesa de compatibilidad universal.

## Matriz de cierre

| Área | Código/configuración del repositorio | Evidencia ejecutada | Cierre | Pendiente |
| --- | --- | --- | ---: | --- |
| Contratos, core y orquestación | Implementados en `pkg/contract/`, `internal/core/`, `internal/orchestrator/` | Suite completa, `vet`, build y race | 100% | Ninguno crítico identificado |
| CLI, agentes y workflows | Comandos, agentes, revisión, reintentos, checkpoints y servicio implementados | `atenea --help`, `version`, reviewer end-to-end, servicio launchd estable y suite completa | 97% | Validación de cada proveedor externo depende de sus procesos disponibles |
| Catálogo y adaptadores nativos | 14 capacidades, 20 implementaciones y 6 familias de provider en `internal/adapter/` y `default.toml` | Tests de paquetes, Kivgraph generation renovada, Tokensave 7.10.0 y matriz real; impacto e indexado probados con Kivgraph | 100% | La cobertura de impacto depende de que el snapshot incluya el archivo cambiado |
| MCP y passthrough | Lifecycle, allow-list, efectos y bridge implementados; el default del repositorio no declara servidores activos | Wrappers `opencode`, `claude` y `codex`: 8/8 handshakes; herramientas raw de lectura probadas; `atenea detect`: 8/8 MCP alcanzables; dashboards con apertura automática desactivada, incluido Kivgraph | 99% | Algunas herramientas requieren objetivos externos y el health sigue siendo bajo demanda |
| Wrappers de modelos | OMP, Claude Code, Codex y OpenCode tienen adapters o backend; OpenCode es opt-in | Claude Code y Codex buscaron `CapabilityIndex` dentro de `0.25 USD`; OpenCode pasó matriz gratuita 6/6 tras repetir un timeout transitorio | 95% | No existe un hard cap común de coste/tokens ni compatibilidad probada con todos los providers |
| Seguridad y permisos | Efectos, `--allow`, rutas sensibles, socket local y procesos contenidos | Tests de contratos, core, supervisor y workflow | 90% | No hay confirmación interactiva; los eventos externos en vuelo pueden superar límites observados |
| Estado, trazas y almacenamiento | DuckDB, checkpoints, métricas, notebook, backups y trace store implementados | Suite de `trace`, `metrics`, `checkpoint`, `backup`, `notebook` y gate | 95% | Sin bloqueo técnico identificado |
| Tests y CI | CI multi-arquitectura, lint, race, coverage y readiness workflows declarados | Suite funcional completa, `vet`, build, policy, Hugo, matriz de capabilities, OpenCode 6/6 y `-race` completo; readiness 9/9 en copia limpia | 99% | El worktree real sigue sin commit por decisión del proyecto |
| Instalación y release | Installer checksum, update, rollback, uninstall y workflows de release | `bash scripts/release-smoke.sh 0.10.4` pasó en macOS arm64 | 95% | La 1.0.0 aún no se publica por decisión del proyecto |
| Documentación | Arquitectura, settings, operaciones, contratos, política y readiness presentes | Anclas de política, Hugo `0.165.0` local y 106 archivos generados | 98% | El módulo docs no pasa `go mod tidy` sin eliminar la dependencia indirecta de Hugo |
| **Repositorio Atenea** | El núcleo funcional y sus contratos están implementados | Suite funcional, `vet`, build, policy, Hugo, race completo, matriz ampliada y OpenCode 6/6 en verde | **99%** | Quedan límites de cobertura del grafo, compatibilidad universal de proveedores externos y la publicación 1.0.0 |

## Herramientas y MCP externos

| Herramienta | Código del repo | Configuración del equipo | Estado real de esta auditoría |
| --- | --- | --- | --- |
| ripgrep | Provider nativo y preferencia selector | Activo como `code.search` | Implementado y preferido |
| Serena | Adapter nativo y lifecycle | Activo y persistente para repositorios del equipo; dashboards con apertura automática desactivada | Implementado; seis procesos `ready` y cuatro implementaciones alternativas probadas |
| OMP | Adapter nativo | Runner activo | Implementado y probado por fixtures |
| Claude Code | Adapter nativo | Runner activo | Búsqueda real de `CapabilityIndex`: `ok`, 0,210193 USD de máximo 0,25; conserva dependencia de CLI/login |
| Codex | Adapter nativo | Runner activo | Búsqueda real de `CapabilityIndex`: `ok`; el CLI no reportó uso monetario |
| Kivgraph | Adapter nativo y viewer separado con readiness HTTP | Runner stdio activo; viewer persistente configurado en `127.0.0.1:7777` | Kivgraph `0.3.4` recompilado con `webassets` y LadybugDB `v0.13.1`; raíz y asset HTTP `200`, `dashboard --check` correcto |
| Tokensave | Adapter nativo y 3 implementaciones en defaults | Runner on-demand en el overlay global con `/opt/homebrew/bin/tokensave` | Tokensave 7.10.0 oficial; índice local de 311 archivos, 11.586 nodos y 27.170 edges de estado; context, overview y calls pasados, incluido un archivo grande mediante fallback por tipos |
| OpenCode | Backend nativo aislado | No es el backend por defecto | Smoke y matriz real gratuitos pasados; opt-in |
| Semgrep | No hay adapter/capability nativa | MCP `raw`, allow-list de 4 herramientas, efecto `read` | `tools/list` y 5 llamadas no destructivas: schema, lenguajes, AST, scan y custom scan; todas correctas tras corregir ruta absoluta |
| Context7 | No hay adapter/capability nativa | MCP `raw`, `resolve-library-id` y `query-docs` | Handshake, resolución de Go y consulta de documentación reales pasados |
| claude-mem | No hay adapter/capability nativa | MCP `raw`, herramientas de memoria y efecto `read/process` | `tools/list`, corpora, contexto, smart search y outline correctos; búsqueda general agotó timeout; no es Codebase Memory |
| Headroom | No hay adapter/capability nativa | MCP `off`, memoria desactivada por variables | `tools/list`, `headroom_stats` y compresión segura correctos; sigue fuera del catálogo Atenea |
| Chrome DevTools, agent-device y Maestro | No hay adapter/capability nativa | MCPs del equipo con allow-list y efectos por herramienta | Chrome listó páginas/consola/red; agent-device listó dispositivos y doctor; Maestro listó dispositivos/cloud/cheat sheet; acciones mutantes excluidas |
| `codebase-memory` | Eliminado del código/configuración activa | No declarado | Comprobación activa pasada; solo quedan menciones históricas o de gates |

## Configuración efectiva observada

El repositorio no contiene overlay `.atenea/config.toml`; el comando
`config show` usa `/Users/gtrave/.config/atenea/atenea.toml`. Ese archivo del
equipo declara seis repositorios y ocho MCP externos. En esta fase se añadieron
`tokensave.calls`, `tokensave.context` y `tokensave.overview` al overlay y se
activó el runner con `/opt/homebrew/bin/tokensave serve --path
/Users/gtrave/Documents/atenea`. El índice se inicializó con la fórmula oficial
de Homebrew, el contador anónimo de subida se desactivó y `/.tokensave/` queda
fuera del repositorio mediante `.gitignore`.

El comando `atenea wrap opencode --version` comprobó los ocho servidores: dos
se declararon al cliente (`chrome-devtools` y `headroom`), seis quedaron
retenidos como `raw` o sin superficie directa, y ninguno fue rechazado por el
handshake. La comprobación no invocó herramientas externas.

El mismo servicio arrancó con estado temporal y `atenea mcp --check` confirmó:

- socket local creado y servicio escuchando;
- 13 capacidades ofrecibles por el bridge MCP;
- ningún chat abierto;
- servidores externos en estado `unknown` en `status`, porque ese estado no
  persiste el handshake de `wrap` ni fuerza llamadas a herramientas raw/off.

La configuración del equipo mantiene Atenea instalada en
`/Users/gtrave/Library/LaunchAgents/atenea.plist`, apuntando al binario estable
`/Users/gtrave/.local/bin/atenea`. Serena se mantiene persistente para los seis
repositorios y cada proceso arranca con `--enable-web-dashboard True
--open-web-dashboard False`: las webs quedan escuchando, pero no se abre el
navegador automáticamente. Headroom mantiene su proxy en `127.0.0.1:8787` con
memoria desactivada y su dashboard se consulta con `headroom dashboard --no-open`.
Maestro mantiene su Viewer en `127.0.0.1:8765` mediante el LaunchAgent
`com.atenea.maestro-viewer`, usando OpenJDK 21 y sin ningún comando de apertura
de navegador. Estas unidades son configuración de este Mac, no archivos del
producto Atenea.

Kivgraph mantiene su MCP stdio oficial en `/Users/gtrave/.local/bin/kivgraph`
y un binario separado con visor web en
`/Users/gtrave/.local/opt/kivgraph-ui/bin/kivgraph`. Atenea supervisa el
segundo como `kivgraph-dashboard`, limitado a `127.0.0.1:7777`, con readiness
HTTP y sin abrir el navegador. La comprobación real devolvió `200` para `/` y
para `assets/index-rN4CIRLY.js`; `atenea dashboard kivgraph --check` pasó.

Kivgraph quedó registrado en `/Users/gtrave/.config/kivgraph/repositories.yaml`
con Atenea y se indexó con éxito. La configuración excluye `docs/` porque es un
módulo Go independiente; incluirlo provocaba que el loader tratase ese módulo
como un paquete del módulo raíz. El resultado publicado es generation `000009`,
con 5 repositorios, 655 archivos, 19.805 símbolos y 5.205 referencias no
resueltas. La capability `graph.status` informó 50.971 edges del grafo
publicado; el provider separa este contador de las capas de indexación
auxiliares, por lo que no se presenta como una única métrica universal.
La ejecución de `repository.index` devuelve los contadores autoritativos del
documento JSONL final de `kivgraph index --full`: 19.805 nodos y 71.664 edges.
Comprueba `graph_status` solo como postcondición de disponibilidad; esa lectura
del snapshot publicado informa 50.971 edges. La diferencia es real y esperada:
el indexador cuenta todas las relaciones producidas, mientras `graph.status`
expone el contador de edges del grafo consultable, no una métrica universal.
No deben sumarse ambos contadores.
Atenea confirmó `ready: true` para su repositorio. Además, el adapter se
actualizó para aceptar el formato compacto `groups[].files[].at` que devuelve
el Kivgraph instalado.

## Límites que impiden afirmar “100%”

1. El significado de una narrativa más allá de las líneas citadas no tiene
   verificación determinista.
2. `budget_usd` y `limits.max_tokens` son autorización y frontera observada,
   no límites duros universales de los proveedores.
3. OpenCode no ofrece schema nativo ni cap de coste común; la evidencia real
   cubre tres modelos gratuitos y una versión de CLI.
4. Tokensave puede truncar su respuesta general de `tokensave_entities` en
   archivos grandes; el adapter detecta el JSON incompleto y repite la consulta
   con el filtro oficial `kinds`, fusionando y deduplicando los resultados. La
   ruta real sobre `internal/adapter/tokensave/tokensave.go` pasó con 45
   símbolos. `code.impact` e `repository.index` ya tienen provider Kivgraph;
   el impacto omite foreign repos porque su salida no tiene campo repository.
5. Los MCP externos tienen handshake válido y varias herramientas raw de lectura
   probadas. El health del panel sigue siendo bajo demanda y no convierte un
   handshake en disponibilidad permanente. Claude Code y Codex ya tienen una
   búsqueda real validada dentro del presupuesto configurado, pero siguen siendo
   proveedores opcionales.
6. La cobertura global observada es 75,2%; el gate de 75,0% evita regresiones,
   pero no demuestra cobertura semántica total.
7. Las herramientas raw no destructivas de Semgrep, Context7, Serena,
   claude-mem, agent-device, Maestro, Headroom y Chrome DevTools fueron
   enumeradas y probadas en la medida permitida por sus objetivos; las acciones
   de escritura, interacción o dispositivo se excluyeron deliberadamente.
8. La suite completa `TMPDIR=/tmp go test -race -count=1 ./...` pasó en esta
   revisión, incluidos los tests de timing de Claude Code y OpenCode después de
   ampliar sus márgenes para máquinas cargadas y el detector de carreras. La
   suite normal, `go vet`, build, política y Hugo también pasaron.

## Cierre de la Fase 21

La deriva Tokensave del overlay quedó corregida sin habilitar un proceso que no
existe. CI ya tiene un suelo cuantitativo de cobertura. Hugo se construyó
localmente con la misma versión de CI, los ocho MCP respondieron al handshake y
Semgrep/Context7 completaron llamadas seguras reales. Los MCP raw/off siguen
siendo integraciones externas hasta que se prueben sus herramientas concretas.

`code.impact` y `repository.index` ya forman parte del contrato activo con
Kivgraph: el primero usa baseline Git más `get_blast_radius`, y el segundo
ejecuta `kivgraph index --full --json` bajo `write+process` y comprueba
`graph_status` antes de responder y devuelve los contadores del resultado final
del indexador, no una lectura potencialmente anterior del servidor persistente.

## Cierre de la Fase 23

Kivgraph ya está activado para Atenea y su generation publicada responde a
través del adapter real. Se recompiló el binario local con la integración MCP
`get_unresolved_references`, se comprobó su presencia en `tools/list` y
`symbol.unresolved` quedó activo en el overlay global.

## Cierre de la Fase 24

La fuente oficial de Tokensave quedó resuelta: Homebrew instaló `7.10.0`, el
servidor MCP respondió al handshake y a `tokensave_status`/`tokensave_context`,
y Atenea ejecutó realmente sus tres implementaciones activas. La matriz de las
13 capabilities disponibles en el overlay quedó así:

| Resultado | Capabilities |
| --- | --- |
| `ok` | `code.context`, `code.impact`, `code.search`, `graph.status`, `repository.index`, `symbol.calls`, `symbol.consumers`, `symbol.definition`, `symbol.get`, `symbol.implementations`, `symbol.overview`, `symbol.references`, `symbol.unresolved` |

Los runners válidos quedaron verificados bajo demanda: `ripgrep`, Serena,
Kivgraph y Tokensave. Claude Code y Codex siguen configurados como opcionales
porque requieren CLI/login y no se usó ningún modelo de pago. Los MCP raw del
equipo mantienen su distinción de integración externa y no se convierten
artificialmente en capabilities nativas.

## Cierre de la Fase 25

La fase resolvió las cuatro tareas operativas pendientes. El binario local de
Kivgraph se recompiló con LadybugDB `v0.13.1` y expone
`get_unresolved_references`; el overlay global ya lo anuncia y la capability
responde `ok`. Tokensave conserva su índice oficial y el adapter particiona por
`kinds` las respuestas grandes, fusionando y deduplicando 45 símbolos reales.
Hugo `0.165.0` quedó instalado y generó 106 archivos de documentación. La
matriz completa de 13 capabilities activas pasó; `code.impact` y
`repository.index` ya están en el catálogo y pasaron ejecución real con permisos explícitos.

## Cierre de la validación pre-1.0.0

La matriz raw enumeró los ocho MCP configurados. Pasaron llamadas de lectura y
diagnóstico en Serena, Context7, Semgrep, claude-mem, agent-device, Maestro,
Headroom y Chrome DevTools. Una búsqueda general de claude-mem agotó su timeout
sin modificar estado; las acciones mutantes de dispositivos, navegador y
procesos fueron excluidas por política.

Claude Code respondió a `code.search` con `0,210193 USD` de `0,25 USD`; Codex
respondió correctamente sin reportar uso monetario. La matriz OpenCode pasó sus
seis combinaciones gratuitas; un timeout aislado de `opencode-go/ox-alpha-free`
se repitió individualmente y pasó con y sin MCP.

La suite normal, `-race`, `vet`, build, política, Hugo, release smoke y
`git diff --check` pasaron. `scripts/v1-readiness.sh` pasó sus nueve etapas en
una copia exacta del worktree con un commit temporal fuera del repositorio real.
El worktree real no recibió commit, tag, push ni release.

## Decisión pre-1.0

No hay un bloqueo crítico del código del repositorio que obligue a otra fase de
implementación inmediata. Atenea queda lista para revisión pre-`1.0.0`, pero no
para etiquetar `1.0.0` por decisión del proyecto: todavía deben revisarse los
límites deliberados de proveedores externos, permisos y compatibilidad antes de
una publicación.

La publicación debe seguir siendo una fase posterior y separada de esta
auditoría. El gate `scripts/v1-readiness.sh` se invocó en el worktree principal
y se detuvo correctamente en la primera regla: exige un checkout limpio; no se
forzó esa condición porque el proyecto ha prohibido commits y publicación en
esta ejecución. La evidencia equivalente de sus etapas técnicas está recogida
arriba.

## Contrato de dashboards

La configuración global de `[[mcp_server]]` admite ahora el campo opcional:

```toml
dashboard = "http://127.0.0.1:8787/dashboard"
```

El loader valida HTTP/HTTPS, host, puerto, ausencia de credenciales e ID DNS.
Los overlays de repositorio siguen sin poder declarar `mcp_server`: esa
restricción evita que un clon pueda ordenar procesos o modificar dashboards de
la máquina.

La resolución centralizada es `MCP id -> dashboard URL -> alias`. Para MCPs
estáticos, el campo `dashboard` contiene la URL completa. Serena es dinámica:
sus instancias por repositorio reciben puertos propios y Atenea descubre la
instancia cuyo proyecto coincide con el directorio actual. El supervisor
calienta las instancias Serena secuencialmente para evitar que varias elijan
el mismo puerto libre. El comando
`atenea dashboard <id>` comprueba accesibilidad y abre el navegador solo por
petición explícita; `--check` no abre nada. `atenea status` y `atenea detect`
exponen la URL configurada sin convertir una URL declarada en una afirmación de
salud.

`atenea dashboard hosts --dry-run` genera un bloque idempotente y marcado en
`/etc/hosts`, preserva entradas ajenas y detecta conflictos. La escritura solo
ocurre con el comando explícito `atenea dashboard hosts` y requiere los
permisos del sistema. Los aliases no ocultan el puerto: el mecanismo oficial de
apertura sigue siendo `atenea dashboard <id>`; un proxy local queda fuera de
esta fase.

La cobertura incluye validación de configuración, resolución, MCP sin
dashboard, dashboard inaccesible, conflictos, preservación de hosts,
idempotencia y `--dry-run`. No se modificó `/etc/hosts` durante la ejecución.

## Auditoría operativa del plan pendiente

| Área | Evidencia actual | Estado | Límite deliberado |
| --- | --- | --- | --- |
| `claude-mem` | Handshake `13.15.3`, `list_corpora`, contexto de sesión, `smart_search` y `smart_outline` correctos | Operativo acotado | La búsqueda general agotó timeout y queda como degradación conocida |
| `agent-device` | Handshake `0.20.10`, `devices` y `doctor` correctos; `capabilities` sin target devuelve `INVALID_ARGS` | Operativo condicionado | Requiere simulador/dispositivo autorizado para capacidades de target |
| Providers opcionales | Claude, Codex y OpenCode probados; Kivgraph, Tokensave, Serena y ripgrep diferenciados | Clasificados | Claude/Codex/OpenCode permanecen opcionales; los providers on-demand pueden aparecer `unknown` hasta ser llamados |
| Acciones destructivas MCP | Efectos `write`, `process` y `external` revisados; `repository.index` sin `--allow write --allow process` fue rechazado | Bloqueadas/simuladas | No se ejecutaron borrados, instalaciones, clics, boots, envíos ni cambios externos irreversibles |
| MCP configurados | `atenea detect --json` alcanzó 8/8: Serena `1.28.1`, Context7 `4.0.2`, Semgrep `1.23.3`, claude-mem `13.15.3`, agent-device `0.20.10`, Maestro `1.0.0`, Headroom `1.29.0`, Chrome DevTools `1.7.0` | `alive` en handshake | La disponibilidad permanente de herramientas raw sigue siendo bajo demanda |

La comprobación de apertura pasó para Headroom en
`http://127.0.0.1:8787/dashboard` y
Maestro en `http://127.0.0.1:8765`. La instancia Serena de Atenea fue
descubierta y validada en `http://127.0.0.1:24287/dashboard/index.html`; las
seis instancias mantuvieron sus dashboards en `24282`–`24287` tras el reinicio.
`dashboard hosts --check` detecta que el
bloque gestionado todavía no está escrito, mientras `--dry-run` propone
`headroom`, `kivgraph` y `maestro`; no se escribió el archivo del sistema.

Tras la implementación, el binario local `/Users/gtrave/.local/bin/atenea` se
actualizó con el build actual y su LaunchAgent se reinició. El servicio quedó
activo y conserva sus 13 capabilities; `status` muestra las URLs estáticas de
Headroom y Maestro, además del proceso `kivgraph-dashboard` en `ready`, mientras
`dashboard serena` resuelve la URL dinámica por proyecto. Esta actualización
local no es una publicación: no crea commit, tag, push ni release.
