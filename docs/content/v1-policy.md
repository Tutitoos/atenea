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
| Permisos | Un efecto no concedido se rechaza antes de ejecutar el trabajo | `pkg/contract/workflow.go`, `internal/core/`, `--allow` | No hay confirmación interactiva; los procesos desatendidos requieren concesión explícita |
| Tiempo | `limits.max_duration` limita el turno mediante el contexto de ejecución | `pkg/contract/assignment.go`, `internal/agent/model/model.go` | La terminación depende de que el proceso externo responda al cierre del contexto |
| Coste | `budget_usd` autoriza y pronostica; el proveedor aplica su propio límite entre mensajes | `internal/agent/planner/`, ayuda de `workflow` | Un mensaje ya iniciado puede superar la previsión |
| Tokens | `limits.max_tokens` se transporta, valida, hereda y estrecha el límite observado de lectura del planner cuando existe una concesión | `pkg/contract/assignment.go`, `internal/agent/planner/`, `internal/agent/model/model.go` | No es un hard cap: un evento en vuelo puede hacer que el uso observado se pase antes de pedir la respuesta final |
| Lectura incremental | `ReadTokens` puede pedir al cliente que deje de leer y produzca una respuesta parcial | `internal/agent/model/model.go` | El uso observado puede llegar con el evento en vuelo; no equivale a impedir cada token posterior |
| Citas | Cada campo de prosa debe aportar al menos una cita `path:line` o `Line N of path`; se comprueban línea, fragmento y ruta realmente abierta, y se conserva la evidencia | `internal/agent/reviewer/citations.go`, `internal/agent/reviewer/citations_test.go`, `internal/config/default.toml` | El significado narrativo más allá de las ubicaciones citadas no es verificable de forma determinista; un renombre con distinto basename queda sin resolver, nunca se adivina |
| OpenCode | Backend opt-in mediante `[model].backend = "opencode"`; exige `step_finish`, texto, JSON único y valida localmente el JSON estructurado | `internal/agent/opencode/`, `internal/agent/model/`, `internal/config/`, `scripts/opencode-smoke.sh`, `scripts/opencode-matrix.sh` | No tiene `--json-schema` ni hard cap de coste común; si reporta coste, Atenea rechaza el resultado por encima del presupuesto solicitado, pero no puede impedir el exceso de un evento en vuelo |
| Búsqueda estructural | `symbol.search` devuelve declaraciones Serena filtradas y ordenadas de forma determinista | `internal/adapter/serena/serena.go`, `symbols.go` | Requiere índice Serena disponible; `code.search` sigue siendo búsqueda textual |
| Cobertura de tests | CI exige al menos 75,0% de cobertura global | `.github/workflows/ci.yml`, `go tool cover` | El umbral es una barrera de regresión, no una prueba de cobertura semántica total |

## Decisiones de no-implementación

Estas decisiones no son fallos silenciosos:

1. No se añade un prompt interactivo de permisos a un daemon o adaptador que
   puede ejecutarse sin una persona presente.
2. OpenCode solo se presenta como provider cuando se selecciona explícitamente
   su backend; el parser propio, la validación local del schema y sus tests son
   obligatorios, y un stream sin terminal no se convierte en una respuesta
   válida.
3. No se convierte `limits.max_tokens` en una falsa garantía. Atenea sí aplica
   ahora el valor como frontera observada del cliente cuando hay una concesión,
   pero la API no tiene un `max_tokens` común que todos los proveedores acepten
   y respeten como límite exacto.
4. La cita es una condición de aceptación para cada campo de prosa. El
   reviewer rechaza campos sin ubicación, líneas inexistentes y fragmentos
   incorrectos; además conserva `cited_path`, `resolved_path`, línea y
   resultado por cita. Esto demuestra la trazabilidad de la evidencia, pero no
   convierte la ubicación en una prueba del significado narrativo completo.
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
- una política de citas basada en un contrato de rutas estable, incluyendo
  renombres y respuestas que mezclen varias fuentes.

## Verificación

El cierre reproducible es:

```sh
bash scripts/v1-readiness.sh
```

Ese gate valida la higiene del árbol, la ausencia de `codebase-memory` activo,
formato, módulos, `vet`, build, tests con race y scripts operativos. La política
de esta página se comprueba además por `scripts/v1-policy-check.sh`.
