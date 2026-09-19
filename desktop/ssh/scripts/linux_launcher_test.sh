#!/usr/bin/env bash
set -euo pipefail

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
cp scripts/linux_launcher.sh "$tmp/atenea-ssh"
chmod +x "$tmp/atenea-ssh"

expect_failure() {
  local expected=$1
  shift
  local status=0
  "$@" >"$tmp/stdout" 2>"$tmp/stderr" || status=$?
  if [[ $status -eq 0 ]] || ! grep -Fq "$expected" "$tmp/stderr"; then
    printf 'Unexpected launcher result (status %s):\n' "$status" >&2
    cat "$tmp/stderr" >&2
    exit 1
  fi
}

expect_failure 'falta el ejecutable' env DISPLAY=:99 "$tmp/atenea-ssh"
cat >"$tmp/atenea-ssh-bin" <<'EOF'
#!/bin/sh
printf 'opened:%s\n' "$1"
EOF
chmod +x "$tmp/atenea-ssh-bin"
expect_failure 'no hay una sesión gráfica' env -u DISPLAY -u WAYLAND_DISPLAY "$tmp/atenea-ssh"
expect_failure 'no hay una sesión gráfica' env DISPLAY=:99 GDK_BACKEND=wayland WAYLAND_DISPLAY=invalid XDG_RUNTIME_DIR="$tmp" "$tmp/atenea-ssh"

mkdir "$tmp/fakebin"
cat >"$tmp/fakebin/ldd" <<'EOF'
#!/bin/sh
printf '%s\n' 'libwebkit2gtk-4.1.so.0 => not found'
exit 1
EOF
chmod +x "$tmp/fakebin/ldd"
expect_failure 'libwebkit2gtk-4.1.so.0' env DISPLAY=:99 PATH="$tmp/fakebin:$PATH" "$tmp/atenea-ssh"

cat >"$tmp/fakebin/ldd" <<'EOF'
#!/bin/sh
printf '%s\n' 'libwebkit2gtk-4.1.so.0 => /usr/lib/libwebkit2gtk-4.1.so.0'
EOF
chmod +x "$tmp/fakebin/ldd"
actual=$(env DISPLAY=:99 PATH="$tmp/fakebin:$PATH" "$tmp/atenea-ssh" test-argument)
[[ "$actual" == 'opened:test-argument' ]]
printf '%s\n' 'Linux launcher diagnostics passed'
