---
title: "Caché de resultados"
---

ATENEA mantiene una caché local, en memoria y acotada, únicamente para
respuestas completas de `code.context` con efecto `read`. La configuración por
defecto es `128` entradas, `4 MiB` y `5m` de vida; `result_cache.max_entries`,
`result_cache.max_bytes` y `result_cache.ttl` se validan al cargar los ajustes.

La clave canónica incluye el permiso completo, repositorio y raíz física,
identidad de fuentes (HEAD, cambios y no versionados), capacidad y versión,
payload y cursor, implementación, proveedor, versión e instancia de la
herramienta, y la evidencia de generación, snapshot y frescura. Por eso un
cambio de código, permiso, proveedor o índice no reutiliza una respuesta vieja.

Las respuestas parciales, truncadas, con error, fuera de alcance o sensibles no
se guardan. Las entradas se expulsan por LRU, tamaño o TTL; llamadas
concurrentes a la misma clave se agrupan. Un acierto conserva el resultado y
su evidencia, marca la versión de caché y no genera otra medición del
proveedor. `declared`, `wired`, `connected` y `tested` siguen siendo estados
distintos: la caché no convierte una configuración o un handshake en una
prueba funcional.
