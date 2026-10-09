#!/usr/bin/env bash
set -euo pipefail

# This is a virtual Wayland render and lifecycle test, not a real desktop test.
shell=build/bin/atenea-ssh
controller=build/bin/atenea-ssh-controller
capture=${ATENEA_SSH_CAPTURE:-build/ci-artifacts/linux-wayland.png}
capture_delay=${ATENEA_SSH_CAPTURE_DELAY:-3}
test_root=$(mktemp -d)
weston_pid=''
first_pid=''
second_pid=''

cleanup() {
  for pid in "$first_pid" "$second_pid" "$weston_pid"; do
    if [[ -n "$pid" ]]; then
      kill "$pid" 2>/dev/null || true
      wait "$pid" 2>/dev/null || true
    fi
  done
  "$controller" --stop >/dev/null 2>&1 || true
  if [[ -f "$test_root/failed" ]]; then
    for name in weston first second screenshooter; do
      if [[ -s "$test_root/$name.log" ]]; then
        echo "=== $name log ===" >&2
        cat "$test_root/$name.log" >&2
      fi
    done
  fi
  rm -rf "$test_root"
}
trap cleanup EXIT
trap 'touch "$test_root/failed"' ERR

export XDG_RUNTIME_DIR="$test_root/runtime"
export XDG_CONFIG_HOME="$test_root/config"
export WAYLAND_DISPLAY=atenea-ssh-test
export GDK_BACKEND=wayland
export XDG_SESSION_TYPE=wayland
unset DISPLAY
mkdir -m 700 "$XDG_RUNTIME_DIR" "$XDG_CONFIG_HOME" "$test_root/pictures"
mkdir -p "$(dirname "$capture")"
capture_dir=$(cd "$(dirname "$capture")" && pwd)
capture_name=$(basename "$capture")
export XDG_PICTURES_DIR="$test_root/pictures"

# Weston debug exposes output capture, safe only on this isolated synthetic CI socket.
weston --no-config --debug --backend=headless --renderer=pixman --socket="$WAYLAND_DISPLAY" --idle-time=0 >"$test_root/weston.log" 2>&1 &
weston_pid=$!
for _ in {1..60}; do
  if [[ -S "$XDG_RUNTIME_DIR/$WAYLAND_DISPLAY" ]]; then break; fi
  kill -0 "$weston_pid"
  sleep 0.5
done
test -S "$XDG_RUNTIME_DIR/$WAYLAND_DISPLAY"

"$shell" >"$test_root/first.log" 2>&1 &
first_pid=$!
"$shell" >"$test_root/second.log" 2>&1 &
second_pid=$!

controller_root="$XDG_CONFIG_HOME/atenea/ssh-desktop"
controller_count() {
  local count=0 proc binary arg root
  local -a args
  for proc in /proc/[0-9]*/cmdline; do
    [[ -r "$proc" ]] || continue
    mapfile -d '' -t args <"$proc" || true
    ((${#args[@]} >= 3)) || continue
    binary=${args[0]}
    arg=${args[1]}
    root=${args[2]}
    if [[ "$binary" == *'/atenea-ssh-controller' && "$arg" == --root && "$root" == "$controller_root" ]]; then
      ((count += 1))
    fi
  done
  printf '%s\n' "$count"
}

for _ in {1..60}; do
  kill -0 "$first_pid"
  kill -0 "$second_pid"
  if [[ $(controller_count) == 1 ]]; then break; fi
  sleep 0.5
done
test "$(controller_count)" == 1
echo 'Wayland: two shell processes share one responsive controller'

sleep "$capture_delay"
weston-screenshooter >"$test_root/screenshooter.log" 2>&1
shopt -s nullglob
screenshots=("$XDG_PICTURES_DIR"/wayland-screenshot-*.png)
if ((${#screenshots[@]} != 1)); then
  cat "$test_root/screenshooter.log" >&2
  echo "Expected one virtual Wayland screenshot, found ${#screenshots[@]}" >&2
  exit 1
fi
mv "${screenshots[0]}" "$capture_dir/$capture_name"
test -s "$capture_dir/$capture_name"
echo "Captured virtual Wayland output: $capture"

kill "$first_pid"
wait "$first_pid" 2>/dev/null || true
first_pid=''
kill -0 "$second_pid"
test "$(controller_count)" == 1
kill "$second_pid"
wait "$second_pid" 2>/dev/null || true
second_pid=''
test "$(controller_count)" == 1

"$controller" --stop
for _ in {1..20}; do
  if [[ $(controller_count) == 0 ]]; then break; fi
  sleep 0.2
done
test "$(controller_count)" == 0
echo 'Wayland: shell close leaves controller running; explicit stop ends it'
