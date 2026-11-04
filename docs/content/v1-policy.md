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
| Permisos | Un efecto no concedido se rechaza antes de ejecutar el trabajo; `--confirm` añade aprobación TTY para `ask` | `pkg/contract/workflow.go`, `internal/core/`, `cmd/atenea/main.go` | La confirmación es opt-in; los procesos desatendidos requieren concesión explícita |
| Coste | `budget_usd` autoriza; el core rechaza cualquier resultado cuyo coste reportado supere el permiso | `internal/orchestrator/`, `internal/core/commission.go` | El proveedor puede haber iniciado trabajo que ya estaba en vuelo; no existe cancelación común por centavos |
| Búsqueda estructural | `symbol.search` se sirve mediante `kivgraph.search` | `internal/config/default.toml` | La disponibilidad operativa depende del proveedor configurado; declarar una implementación no certifica su salud |
| Cobertura de tests | CI ejecuta la suite con perfil de cobertura y publica la medición y los artefactos por plataforma, sin convertir un porcentaje en requisito de paso | `.github/workflows/ci.yml` | La cobertura cuenta sentencias ejecutadas y sirve como señal informativa, no como prueba de cobertura semántica total |

## Decisiones de no-implementación

Estas decisiones no son fallos silenciosos:

1. No se añade un prompt interactivo de permisos al daemon o a un adaptador.
   Solo los comandos directos solicitan `--confirm`, y fallan si no hay TTY.
2. `code.impact` y `repository.index` tienen provider Kivgraph. El primero
   compara un baseline Git con la copia actual y devuelve impacto acotado al
   repositorio; el segundo ejecuta el indexador oficial con permisos explícitos
   `write+process` y verifica el snapshot publicado antes de responder.

> Los agentes integrados (`atenea agent`), su revisión por citas, la revisión
> semántica y el backend OpenCode se midieron en el momento de esta política; ya
> no forman parte de Atenea (véase #212). Sus garantías se retiraron con ellos.

## Criterios para v1.1

Una futura ampliación puede cambiar cualquiera de estas decisiones solo si
aporta, junto con código y tests:

- una señal de uso/cancelación común y una semántica documentada para cada
  provider;
- un flujo de permisos interactivo con identidad de sesión, timeout y modo no
  interactivo explícito.

## Verificación

El cierre reproducible es:

```sh
bash scripts/v1-readiness.sh
```

Ese gate valida la higiene del árbol, la ausencia de referencias al backend
retirado, formato, módulos, `vet`, build, tests con race y scripts operativos. La política
de esta página se comprueba además por `scripts/v1-policy-check.sh`.
