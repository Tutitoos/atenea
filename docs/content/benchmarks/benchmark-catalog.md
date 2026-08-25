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

## Procedencia de estas medidas

Observación fechada: estas cinco filas proceden del perfil `quick` ejecutado el
2026-08-22T23:44:54Z sobre el commit `9630fd4d`, con tres procesos independientes
por benchmark. **No se han vuelto a medir en la remediación del 2026-08-25.** Las
tablas de tests y cobertura de las otras páginas sí se regeneraron ese día, así
que ambas cifras no comparten ejecución.

El motivo es que no existe forma de refrescar sólo esta tabla: el único productor
es `go run ./cmd/atenea-benchmark --profile quick --benchmark-runs 3`, que
reescribe además `benchmarks/summary.json`, `benchmarks/summary.md` y
`docs/data/benchmarks/latest.json` en la misma pasada. Inventar aquí un ns/op
sería exactamente lo que `benchmarks/README.md` prohíbe cuando pide que un
informe se pueda auditar sin fiarse de una tabla editada a mano.

Lo que sí se verificó contra el árbol actual es el conjunto de benchmarks: los
cinco nombres, sus categorías y sus paquetes siguen coincidiendo uno a uno con
las especificaciones declaradas en `cmd/atenea-benchmark/main.go:268-272`, de
modo que la tabla no lista escenarios que ya no existan. Lo que puede haber
envejecido son los valores, no el catálogo.

Comparar estas cifras con otra máquina o con otro perfil no es válido: el
criterio de comparabilidad del proyecto exige mismo benchmark, mismo perfil,
mismo dataset y hardware compatible.
