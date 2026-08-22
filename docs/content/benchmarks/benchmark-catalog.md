---
title: Benchmark catalog
weight: 3
dashboard: catalog
---

# Catálogo de benchmarks

| Benchmark | Categoría | Muestras | ns/op | B/op | Allocs/op | Throughput/s | RSS | CPU ms | CV | Delta | Estado |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|
| select-medium-catalog | micro | 3 | 25533.0 | 74240.0 | 15.0 | 39165.0 | 94257152 | 624.0 | 9.7% | 0.0% | 🟠 ORANGE |
| record-measurement | micro | 3 | 15958.0 | 224.0 | 1.0 | 62664.5 | 274972672 | 1399.9 | 11.1% | 0.0% | 🟠 ORANGE |
| flush-persisted-measurement | persistence | 3 | 7394577.0 | 5802.0 | 166.0 | 135.2 | 275431424 | 1654.5 | 2.9% | 0.0% | 🟠 ORANGE |
| plan-layers-medium-dag | micro | 3 | 154968.0 | 162774.0 | 303.0 | 6452.9 | 96600064 | 656.1 | 8.5% | 0.0% | 🟠 ORANGE |
| run-plan-concurrent-medium-dag | load | 3 | 270733083.0 | 3098560.0 | 9726.0 | 3.7 | 280182784 | 1697.8 | 0.9% | 0.0% | 🟠 ORANGE |

Los resultados detallados se conservan en benchmarks/runs/latest/.
