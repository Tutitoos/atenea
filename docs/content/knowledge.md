---
title: Conocimiento verificado
weight: 10
---

# Conocimiento verificado

P15 añade `internal/knowledge`, un almacén SQLite separado de `notebook`,
historial y cachés. Cada entrada pertenece a un proyecto y repositorio,
declara su tipo (`decision`, `convention`, `solution`, `hypothesis` o `fact`),
fuentes con identidad y dependencias con generación/proveedor cuando aplica.

Las entradas nacen como `candidate`. El `AcceptanceGate` productivo delega en
`PromoteVerified`, que consulta un `EvidenceResolver` autoritativo del workflow;
el caller no puede insertar receipts ni pasar booleanos. Un punto aceptado,
revisión de Sol, auditoría de Astra y sus digests verificables deben pertenecer
al mismo punto, árbol, modelo y scope. Fallos, resultados parciales, evidence
falsificada o pruebas sin completar no satisfacen ese gate. Un cambio de dependencia marca
la entrada `stale`; una decisión reemplazada conserva el evento
`superseded`. Todas las transiciones se escriben en `knowledge_events` de
forma append-only dentro de la misma transacción SQLite.

El almacén comprueba sujeto, proyecto, repositorio y visibilidad antes de cada
lectura o mutación; las entradas privadas solo son visibles para su propietario
y las de proyecto para miembros autorizados. `ContextProvider.Prepare` consulta
únicamente conocimiento `verified` y revalida cada entrada con dependencias
`fresh` dentro de su TTL mediante un probe autorizado antes de devolverla;
candidates, stale y `legacy_unverified` quedan fuera. Las revalidaciones
comprueban fuentes, snapshots, generaciones y receipts de probe completos, y
aplican CAS sobre el estado leído. Core expone esta ruta cuando
`[knowledge].enabled` está activo o se le configura explícitamente un Store P15.
Las escrituras usan transacciones,
WAL, `synchronous=FULL`, busy timeout y reintentos acotados. `Backup` y
`Restore` trabajan sobre copias SQLite verificables.

La integración normal se activa con:

```toml
[knowledge]
enabled = true
# path y workflow_path son opcionales; por defecto quedan bajo el estado de Atenea.
```

Core abre el almacén y el workflow al arrancar y registra la herramienta MCP
read-only `knowledge.context`. La herramienta exige `scope.project_id` y
`scope.repository_id`, deriva el sujeto de la sesión y devuelve solo entradas
`verified` cuya revalidación del workflow es completa y fresca. Si falta el
workflow, el snapshot o una identidad verificable, la entrada se omite; una
instalación sin `[knowledge]` no anuncia la herramienta. MCP no expone APIs de
escritura ni permite insertar receipts.

Los datos anteriores no se migran ni se consideran hechos: siguen siendo
`legacy_unverified` en sus interfaces existentes. El almacén usa un esquema
versionado, eventos append-only protegidos por triggers, copias atómicas con
`integrity_check` y bases con permisos 0600; una versión incompatible se
rechaza.

Durante una ejecución, solo un paso con rol `implement`, resultado completo y
un objeto explícito `knowledge_candidate` puede crear un candidato:

```json
{
  "knowledge_candidate": {
    "kind": "fact",
    "title": "Un escritor por worktree",
    "body": "El coordinador conserva un único escritor activo.",
    "visibility": "project"
  }
}
```

El host fija repositorio, propietario, fuente, proveedor y fingerprint; el
agente no puede suministrarlos. En esta primera versión el identificador del
repositorio también delimita el proyecto porque el contrato del workflow aún
no transporta un proyecto separado. Un candidato inválido convierte el paso
en fallo antes de cualquier promoción. La promoción automática ocurre después
de persistir el checklist aceptado y exige la cadena completa de
implementación, revisión Sol y auditoría Astra sobre el mismo fingerprint.
