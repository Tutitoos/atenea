# Atenea para Claude Desktop

Extensión MCPB que conecta Claude Desktop con el núcleo local de Atenea.

El proceso de la extensión ejecuta `atenea mcp`, que es un puente: no arranca un
segundo núcleo. El servicio central conserva el catálogo, el workflow, la
selección de proveedores y los permisos, y la extensión habla con él por el
socket de siempre.

## Requisitos

- Atenea instalada y a la misma versión que declara `manifest.json`.
- El servicio activo antes de abrir una conversación que use las herramientas.
- El empaquetador [`mcpb`](https://github.com/anthropics/mcpb) en el `PATH`, o
  su ruta en `MCPB_BIN`.

## Construcción

```sh
bash scripts/build-claude-mcpb.sh
```

El script construye el binario desde este checkout, comprueba que la versión que
reporta coincide con la del manifiesto y deja el artefacto en
`dist/atenea-<versión>.mcpb`. Dos variables lo desvían cuando hace falta:
`ATENEA_BINARY` empaqueta un binario ya existente —un artefacto de release
descargado, por ejemplo— y `MCPB_OUTPUT` cambia el destino. La comprobación de
versión se aplica igual en ambos casos: un `.mcpb` cuyo manifiesto dice una
versión y cuyo binario reporta otra es el fallo que parece una instalación
correcta hasta que alguien abre un bug contra el código equivocado.

## Instalación

En Claude Desktop: `Settings → Extensions → Advanced settings → Install
Extension…`. Después, verificarla desde `+ → Connectors`.

## Sobre el entorno

El manifiesto no fija `XDG_STATE_HOME`. Fijarlo parecía prudente y no lo era:
`platform.StateDir()` ya cae a `$HOME/.local/state/atenea` cuando la variable no
está, de modo que fijarla no aportaba nada en el caso normal — y en el de un
usuario que sí la tiene puesta en otro sitio, apuntaba la extensión a un socket
distinto del que usa su servicio. La extensión hereda el entorno como cualquier
otro `atenea mcp`.
