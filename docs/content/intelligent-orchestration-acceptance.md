# Aceptación de la orquestación inteligente

Fecha de cierre local: 7 de septiembre de 2026.

Esta entrega se preparó en un worktree aislado, sobre la rama `feat/atenea-intelligent-orchestration`. La rama parte del commit `9ab1c03e9dca5e2167f582d9f860aae14e23424d`. El checkout principal permaneció limpio en `main` y en el mismo commit durante la implementación.

Se instalaron localmente las dependencias del dashboard con autorización para ejecutar sus pruebas y build. En el momento de cerrar la aceptación local aún no se había realizado commit ni push. No se instaló ATENEA ni se realizó merge, despliegue o migración.

## Resultado por punto

| Punto | Resultado local | Evidencia principal |
|---|---|---|
| P00 | Aceptado | Rama exacta, worktree independiente y checkout principal limpio. |
| P01 | Aceptado | Corpus fijo `benchmarks/corpus/v1`, esquema, manifiesto y digest SHA-256. |
| P02 | Aceptado | Backend nativo Codex, perfiles fijos, esfuerzo solicitado/observado, denegación por defecto y ausencia de sustitución automática. |
| P03 | Aceptado | Creación, estado, pregunta, cancelación y reanudación persistentes con prevención de efectos duplicados. |
| P04 | Aceptado | Políticas persistidas, grants, límites globales y exclusión de escritores simultáneos por worktree. |
| P05 | Aceptado | Avisos Markdown persistidos antes de invocar agentes o herramientas, con cursores y publicación en el canal principal. |
| P06 | Aceptado | Checklist y barra deterministas, panel expandible, SSE más sondeo acotado, telemetría por punto y estados medido, estimado, parcial y desconocido. |
| P07 | Aceptado | `code.context` agrupa las fuentes y evita lecturas redundantes conservando cobertura y paginación. |
| P08 | Aceptado | Adaptación central de MCP `2025-06-18` y `2026-07-28`, incluidos IDs, cabeceras, transporte y cancelación. |
| P09 | Aceptado localmente | Matriz de clientes y contratos; los clientes sin actividad intermedia permanecen parciales. |
| P10 | Aceptado localmente | Piloto de fallos controlados con límites, cancelación, continuidad y reconstrucción autorizada. |
| P11 | Aceptado | Caché ligada a fuente, generación, proveedor y permisos; selección por calidad observada. |
| P12 | Aceptado | `workspace.context` conserva repositorio, procedencia, permisos, orden y resultados parciales. |
| P13 | Aceptado localmente | `symbol.implementations` para Go y TypeScript distingue relaciones exactas y candidatos. |
| P14 | Aceptado | Dart documenta contexto disponible e implementaciones semánticas no soportadas. |
| P15 | Aceptado | Conocimiento candidato/aceptado, fuentes, vigencia y revalidación semántica. |
| P16 | Aceptado | Propuestas MCP con evidencia, digest, bloqueo multiproceso y aplicación explícita al destino vinculado. |
| P17 | Aceptado localmente | Aceptación v9: cinco gates y una comparación aprobados, con doce escenarios enlazados a fixtures selladas. |
| P18 | Aceptado localmente | Informe final, verificaciones globales, revisión Sol y auditoría Astra completadas sin bloqueadores materiales. |

## Evidencia de aceptación P17

El artefacto durable está en `benchmarks/runs/intelligent-orchestration-2026-09-07/`. Registra:

- commit base `9ab1c03e9dca5e2167f582d9f860aae14e23424d`;
- digest de fuentes `4eb862618ef67308cd794f0bd110b48e0bda2386cbbbf6047027b33f318f2f46` para el árbol evaluado antes de incorporar este informe;
- digest del corpus `bb57b8555c44fdd754bc72855c7899adda318677b48ee5696820a710efec98a5`;
- cinco gates locales aprobados y una comparación aprobada;
- S01–S12 vinculados a bytes y SHA-256 exactos, con `run` y `pass` obligatorios para cada prueba declarada.

La ejecución v8 registró un fallo previo del gate `tracking`. Su evidencia quedó truncada y no permite identificar la prueba. Una reproducción inmediata del gate completo y la ejecución v9 pasaron sin cambios de fuentes. Se conserva como inestabilidad observada sin causa determinada.

## Mediciones

| Métrica | Antes | Después | Estado | Comparable | Interpretación |
|---|---:|---:|---|---|---|
| Lecturas del microbenchmark | 2400 | 1800 | Medido localmente | Sí | Doce accesos repetidos frente a nueve fixtures únicas. |
| Latencia del microbenchmark | Registrada en el artefacto | Registrada en el artefacto | Medido localmente | Sí | Medición de fixture, no latencia de proveedor. |
| Llamadas de `code.context` | 22 | 2 | Parcial local | No | El valor anterior es una estimación derivada del camino histórico; el posterior se observó en fixture. |
| Intervenciones humanas del corpus | 0 | 0 | Medido localmente | Sí | No incluye autorizaciones externas al ejecutor. |
| Tokens | Desconocido | Desconocido | Pendiente | No | No se convirtió ausencia de datos en cero. |
| Coste de proveedor | Desconocido | Desconocido | Pendiente | No | No hubo prueba facturada de proveedor real. |
| Sobrecoste en cliente real | Desconocido | Desconocido | Pendiente | No | Requiere validación visual y funcional en cada cliente. |

No se declara un porcentaje de mejora. Las únicas comparaciones aceptadas son las que el artefacto marca como comparables.

## Alcance de la evidencia

| Nivel | Estado |
|---|---|
| Implementado en la rama | Sí. |
| Pruebas unitarias, integración, carreras, lint, tipos y build local | Sí; P18 aceptado localmente. |
| Proveedores reales | Pendiente. Las pruebas usan adaptadores y proveedores simulados salvo evidencia que declare otra cosa. |
| Chat real de Codex/ChatGPT, Claude, Oh My Pi y OpenCode | Pendiente. S12 acredita API, SSE y formato local; no acredita renderizado real. |
| Dart semántico | No soportado y declarado como tal. |
| Dependencias de desarrollo del dashboard | Instaladas localmente con autorización para pruebas y build. |
| Commit y push | No realizados durante la aceptación local; corresponden al paso posterior de publicación. |
| Instalación de ATENEA, merge, despliegue o migración | No realizado. |

La aceptación global local quedó completada: las verificaciones finales pasaron y Sol y Astra no encontraron bloqueadores materiales en el árbol completo. Esto no amplía la evidencia pendiente de proveedores y clientes reales.
