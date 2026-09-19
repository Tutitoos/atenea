#!/bin/sh
set -eu

launcher=$(command -v "$0")
binary=$(dirname -- "$launcher")/atenea-ssh-bin
if [ ! -x "$binary" ]; then
  printf '%s\n' 'Atenea SSH: falta el ejecutable de la ventana junto al lanzador. Reinstala el paquete completo.' >&2
  exit 78
fi

wayland_socket=''
if [ -n "${WAYLAND_DISPLAY:-}" ]; then
  case "$WAYLAND_DISPLAY" in
    /*) wayland_socket=$WAYLAND_DISPLAY ;;
    *) wayland_socket=${XDG_RUNTIME_DIR:-}/$WAYLAND_DISPLAY ;;
  esac
fi
if { [ -z "${DISPLAY:-}" ] || [ "${GDK_BACKEND:-}" = wayland ]; } &&
   { [ -z "$wayland_socket" ] || [ ! -S "$wayland_socket" ]; } ||
   { [ "${GDK_BACKEND:-}" = x11 ] && [ -z "${DISPLAY:-}" ]; }; then
  printf '%s\n' 'Atenea SSH: no hay una sesión gráfica X11 o Wayland disponible. Inicia sesión en el escritorio de este usuario y vuelve a abrir la aplicación.' >&2
  exit 69
fi

if ! command -v ldd >/dev/null 2>&1; then
  printf '%s\n' 'Atenea SSH: no se puede comprobar las bibliotecas gráficas (falta ldd).' >&2
  exit 69
fi
ldd_status=0
dependencies=$(ldd "$binary" 2>&1) || ldd_status=$?
missing=$(printf '%s\n' "$dependencies" | awk '/not found/ { print $1 }')
if [ -n "$missing" ]; then
  printf '%s\n' 'Atenea SSH: faltan bibliotecas gráficas:' "$missing" >&2
  printf '%s\n' 'Instala los paquetes de GTK3 y WebKit2GTK 4.1 de tu distribución. En Ubuntu 24.04: sudo apt install libgtk-3-0 libwebkit2gtk-4.1-0' >&2
  exit 69
fi
if [ "$ldd_status" -ne 0 ]; then
  printf '%s\n' 'Atenea SSH: no se pudieron comprobar las bibliotecas de la ventana. Comprueba la arquitectura y las dependencias del paquete.' >&2
  exit 69
fi

exec "$binary" "$@"
