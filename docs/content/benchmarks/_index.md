---
title: Benchmarks
weight: 8
dashboard: overview
---

# Benchmarks y métricas

Última ejecución de tests: **2026-08-25T01:13:07Z** · Comando: **`go test -json -count=1 -coverprofile ./...`** · Commit: **49ebdbb9538d6e08b90a100b504247fa5675e069**.

Entorno: **MacBook Air M5**, 24 GB, **darwin/arm64**, Go **go1.26.7**.

Estado global: **🟠 ORANGE**.

Árbol de trabajo: **DIRTY**. Las ejecuciones con cambios locales no se usan como baseline de release.

| Tests ejecutados | Pasados | Fallidos | Omitidos | Cobertura | Estado |
|---:|---:|---:|---:|---:|---|
| 2126 | 2126 | 0 | 10 | 80.4% | 🟠 ORANGE |

- [Metrics](metrics/)
- [Test inventory](test-inventory/)
- [Benchmark catalog](benchmark-catalog/)

## Qué mezcla esta portada

Los tests y la cobertura son del 2026-08-25. El catálogo de benchmarks sigue
siendo el del perfil `quick` del 2026-08-22 sobre el commit `9630fd4d`: no se
ha vuelto a medir porque el único productor, `go run ./cmd/atenea-benchmark`,
reescribiría en la misma pasada `benchmarks/summary.json` y
`docs/data/benchmarks/latest.json`. La portada mezcla por tanto dos
ejecuciones, y el estado global 🟠 ORANGE hereda tanto los paquetes por debajo
del objetivo del 80 % como el estado naranja de los cinco benchmarks.

El global es ORANGE y no GREEN aun con 0 fallos y 80,4 % de cobertura porque
`OverallStatus` degrada en cuanto un paquete queda por debajo del objetivo o
declara skips: 16 de los 51 paquetes quedan naranjas y hay 10 tests omitidos.

Aviso para quien lea el sitio publicado y no el repositorio: cuando existe
`docs/data/benchmarks/latest.json`, `layouts/partials/benchmark-dashboard.html`
renderiza las cuatro páginas desde ese JSON e ignora el markdown. Actualizar
estas tablas no actualiza el sitio; hace falta un run real que reescriba
`latest.json`.
