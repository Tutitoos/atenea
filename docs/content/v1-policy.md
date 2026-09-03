---
title: v1.0 policy
weight: 9
---

# Política de Atenea v1.0

Esta página es el cierre operativo de los contratos que podían confundirse con
funcionalidades incompletas. La regla es sencilla: una garantía solo se anuncia
si Atenea puede observarla y hacerla cumplir de forma determinista en todos los
proveedores soportados.

## Matriz de garantías

| Área | Garantía v1.0 | Evidencia | Límite conocido |
| --- | --- | --- | --- |
| Permisos | Un efecto no concedido se rechaza antes de ejecutar el trabajo; `--confirm` añade aprobación TTY para `task` y `ask` | `pkg/contract/workflow.go`, `internal/core/`, `cmd/atenea/main.go` | La confirmación es opt-in; los procesos desatendidos requieren concesión explícita |
| Tiempo | `limits.max_duration` limita el turno mediante el contexto de ejecución | `pkg/contract/assignment.go`, `internal/agent/model/model.go` | La terminación depende de que el proceso externo responda al cierre del contexto |
| Coste | `budget_usd` autoriza; el core rechaza cualquier resultado cuyo coste reportado supere el permiso | `internal/orchestrator/`, `internal/core/commission.go`, `internal/agent/opencode/opencode.go` | OpenCode interrumpe el proceso al observar un evento por encima del presupuesto, pero el proveedor puede haber iniciado trabajo que ya estaba en vuelo; no existe cancelación común por centavos |
| Tokens | `limits.max_tokens` se transporta, valida, hereda y estrecha el límite observado; los streams interrumpen el proceso cuando ya pueden observar un exceso | `pkg/contract/assignment.go`, `internal/agent/planner/`, `internal/agent/model/model.go`, `internal/agent/opencode/opencode.go` | Sigue sin ser un hard cap del proveedor: un evento en vuelo puede hacer que el uso observado se pase antes de que Atenea pueda detenerlo |
| Lectura incremental | `ReadTokens` puede pedir al cliente que deje de leer y produzca una respuesta parcial | `internal/agent/model/model.go` | El uso observado puede llegar con el evento en vuelo; no equivale a impedir cada token posterior |
| Citas | Cada campo de prosa debe aportar al menos una cita `path:line` o `Line N of path`; se comprueban línea, fragmento y ruta realmente abierta, y se conserva la evidencia | `internal/agent/reviewer/citations.go`, `internal/agent/reviewer/citations_test.go`, `internal/config/default.toml` | El significado narrativo más allá de las ubicaciones citadas no es verificable de forma determinista; un renombre con distinto basename queda sin resolver, nunca se adivina |
| Revisión semántica | `semantic-reviewer` puede auditar de forma opt-in si una conclusión se sigue de la evidencia y devuelve `supported`, `unsupported` o `indeterminate` con confianza y alcance | `internal/agent/semanticreviewer/`, `cmd/atenea/agent.go`, `internal/config/default.toml` | Es una revisión model-backed, no una garantía determinista: consume el presupuesto de `explore`, puede ser indeterminada y nunca sustituye la revisión de citas |
| OpenCode | Backend opt-in mediante `[model].backend = "opencode"`; exige `step_finish`, texto, JSON único, valida localmente el JSON estructurado y detiene el proceso tras observar un exceso de tokens/coste | `internal/agent/opencode/`, `internal/agent/model/`, `internal/config/`, `scripts/opencode-smoke.sh`, `scripts/opencode-matrix.sh` | No tiene `--json-schema` ni hard cap de coste común; el exceso de trabajo ya iniciado por el proveedor no puede deshacerse |
| Búsqueda estructural | `symbol.search` conserva su contrato sin proveedor | `internal/config/default.toml` | No se ofrece en `tools/list`, igual que `symbol.implementations` y `symbol.unresolved` |
| Cobertura de tests | CI ejecuta la suite con perfil de cobertura y publica la medición y los artefactos por plataforma, sin convertir un porcentaje en requisito de paso | `.github/workflows/ci.yml` | La cobertura cuenta sentencias ejecutadas y sirve como señal informativa, no como prueba de cobertura semántica total |

## Decisiones de no-implementación

Estas decisiones no son fallos silenciosos:

1. No se añade un prompt interactivo de permisos al daemon o a un adaptador.
   Solo los comandos directos solicitan `--confirm`, y fallan si no hay TTY.
2. OpenCode solo se presenta como provider cuando se selecciona explícitamente
   su backend; el parser propio, la validación local del schema y sus tests son
   obligatorios, y un stream sin terminal no se convierte en una respuesta
   válida.
3. No se convierte `limits.max_tokens` en una falsa garantía. Atenea sí aplica
   ahora el valor como frontera observada del cliente y detiene los streams
   cuando cruza esa frontera, pero la API no tiene un `max_tokens` común que
   todos los proveedores acepten y respeten como límite exacto.
4. La cita es una condición de aceptación para cada campo de prosa. El
   reviewer rechaza campos sin ubicación, líneas inexistentes y fragmentos
   incorrectos; además conserva `cited_path`, `resolved_path`, línea y
   resultado por cita. Esto demuestra la trazabilidad de la evidencia, pero no
   convierte la ubicación en una prueba del significado narrativo completo.
   Cuando se necesita esa capa, `--review semantic-reviewer` la añade de forma
   explícita, con un modelo configurado, confianza y posibilidad de
   `indeterminate`; nunca se oculta el coste dentro del reviewer determinista.
5. `code.impact` y `repository.index` tienen provider Kivgraph. El primero
   compara un baseline Git con la copia actual y devuelve impacto acotado al
   repositorio; el segundo ejecuta el indexador oficial con permisos explícitos
   `write+process` y verifica el snapshot publicado antes de responder.

## Criterios para v1.1

Una futura ampliación puede cambiar cualquiera de estas decisiones solo si
aporta, junto con código y tests:

- una señal de uso/cancelación común y una semántica documentada para cada
  provider;
- un flujo de permisos interactivo con identidad de sesión, timeout y modo no
  interactivo explícito;
- un contrato de coste y schema nativo de OpenCode que permita endurecer las
  garantías más allá del uso observado;
- una política de selección y presupuesto para hacer `semantic-reviewer`
  obligatorio en superficies concretas, en lugar de mantenerlo opt-in;
- una política de citas basada en un contrato de rutas estable, incluyendo
  renombres y respuestas que mezclen varias fuentes.

## Verificación

El cierre reproducible es:

```sh
bash scripts/v1-readiness.sh
```

Ese gate valida la higiene del árbol, la ausencia de referencias al backend
retirado, formato, módulos, `vet`, build, tests con race y scripts operativos. La política
de esta página se comprueba además por `scripts/v1-policy-check.sh`.
