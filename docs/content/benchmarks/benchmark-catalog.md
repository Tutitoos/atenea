---
title: Benchmark catalog
weight: 3
dashboard: catalog
---

# Catálogo de benchmarks

| Benchmark | Categoría | Muestras | ns/op | B/op | Allocs/op | Throughput/s | RSS | CPU ms | CV | Delta | Estado |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|
| select-medium-catalog | micro | 3 | 35429.0 | 74242.0 | 15.0 | 28225.5 | 93962240 | 631.6 | 9.2% | 0.0% | 🟠 ORANGE |
| record-measurement | micro | 3 | 17167.0 | 224.0 | 1.0 | 58251.3 | 274874368 | 1574.5 | 8.5% | 0.0% | 🟠 ORANGE |
| flush-persisted-measurement | persistence | 3 | 16188756.0 | 6016.0 | 167.0 | 61.8 | 275775488 | 2465.0 | 36.1% | 0.0% | 🟠 ORANGE |
| plan-layers-medium-dag | micro | 3 | 249781.0 | 162773.0 | 303.0 | 4003.5 | 94846976 | 665.9 | 3.9% | 0.0% | 🟠 ORANGE |
| run-plan-concurrent-medium-dag | load | 3 | 320015458.0 | 3000776.0 | 9655.0 | 3.1 | 275562496 | 2328.8 | 2.6% | 0.0% | 🟠 ORANGE |

Los resultados detallados se conservan en benchmarks/runs/latest/.
