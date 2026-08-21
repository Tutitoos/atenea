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
| Citas | Las citas `path:line` se comprueban y las citas con fragmento adyacente pueden verificar contenido | `internal/agent/reviewer/citations.go` | No se exige una cantidad mínima: abreviaturas, renombres y rutas compuestas no tienen una política segura común |
| OpenCode | Backend de modelo opt-in mediante `[model].backend = "opencode"`; exige evento `step_finish` y texto antes de aceptar | `internal/agent/opencode/`, `internal/agent/model/`, `internal/config/` | No tiene `--json-schema` ni hard cap de coste común; uso/coste son observados y el stream puede quedar incompleto |
| Búsqueda estructural | `symbol.search` devuelve declaraciones Serena filtradas y ordenadas de forma determinista | `internal/adapter/serena/serena.go`, `symbols.go` | Requiere índice Serena disponible; `code.search` sigue siendo búsqueda textual |

## Decisiones de no-implementación

Estas decisiones no son fallos silenciosos:

1. No se añade un prompt interactivo de permisos a un daemon o adaptador que
   puede ejecutarse sin una persona presente.
2. OpenCode solo se presenta como provider cuando se selecciona explícitamente
   su backend; el parser propio y sus tests son obligatorios, y un stream sin
   terminal no se convierte en una respuesta válida.
3. No se convierte `limits.max_tokens` en una falsa garantía. Atenea sí aplica
   ahora el valor como frontera observada del cliente cuando hay una concesión,
   pero la API no tiene un `max_tokens` común que todos los proveedores acepten
   y respeten como límite exacto.
4. No se convierte la presencia de una cita en prueba de la narrativa completa.
   El reviewer verifica ubicaciones y fragmentos, no el significado de cada
   afirmación.

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
