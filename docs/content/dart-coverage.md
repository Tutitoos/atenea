---
title: Dart capability coverage
weight: 9
---

# Cobertura Dart

P14 mantiene una matriz independiente para Dart. El reporte distingue tres
hechos: que Atenea declara una capacidad, que existe una conexión observada y
que una ejecución funcional fue validada. `unknown` significa que la
observación no se hizo; `unsupported` significa que la combinación está
rechazada deliberadamente.

La ejecución reproducible es:

```text
go run ./cmd/atenea-dart-coverage
```

Escribe `benchmarks/runs/dart-coverage-2026-09-06/report.json` y su Markdown.
Lee los fixtures propios de `internal/dartcoverage/testdata` y vuelve a
validar S03 y S07 contra el SHA fijo de `benchmarks/corpus/v1`. El corpus P01
se abre en modo lectura y su SHA no se modifica.

El reporte fija como base observada Kivgraph
`e28323742c8f148a859dcc54045727629ab4ba8e` (`main`). Atenea consume la
herramienta MCP nativa; esta referencia no certifica una conexión del proveedor
ni habilita `symbol.implementations` para Dart.

| Capacidad | Estado local | Conexión | Alcance |
|---|---|---|---|
| `code.context` | `proven` | `unknown` | Ruta productiva `Runner` con respuesta `Session` local simulada |
| `workspace.context` | `proven` | `unknown` | Coordinador productivo con dos hijos e identidades separadas; respuestas simuladas |
| `symbol.implementations` | `unsupported` | `unknown` | No habilitado en Atenea; rechazo Dart antes del proveedor y E2E real pendiente |

Las pruebas de componente observadas en la revisión de referencia muestran que
Kivgraph genera evidencia Dart `IMPLEMENTS`/`OVERRIDES`, pero el intento real de
CLI llegó a `kivgraph index --full` y falló porque `LadybugDB native support is
unavailable`; no se publicó una generación. Esa capacidad de componente no se
convierte en una conexión E2E de Atenea, un proveedor real ni una presentación
en el chat de un cliente. Los niveles `real_provider` y `real_client`
permanecen pendientes. La matriz no prueba la interpretación real de fuentes
Dart en Atenea: esa validación queda pendiente.
