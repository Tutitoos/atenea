#!/usr/bin/env bash
set -euo pipefail

shell=build/bin/atenea-ssh
controller=build/bin/atenea-ssh-controller
capture=build/bin/linux-render-smoke.png
log_file=$(mktemp)
app_pid=''

cleanup() {
  if [[ -n "$app_pid" ]]; then
    kill "$app_pid" 2>/dev/null || true
    wait "$app_pid" 2>/dev/null || true
  fi
  "$controller" --stop >/dev/null 2>&1 || true
  if [[ -s "$log_file" ]]; then cat "$log_file"; fi
  rm -f "$log_file"
}
trap cleanup EXIT

"$shell" >"$log_file" 2>&1 &
app_pid=$!
window_id=''
for _ in {1..60}; do
  if ! kill -0 "$app_pid" 2>/dev/null; then
    echo 'Atenea SSH exited before a window appeared' >&2
    exit 1
  fi
  window_id=$(xdotool search --onlyvisible --name '^Atenea SSH$' | head -n 1 || true)
  if [[ -n "$window_id" ]]; then break; fi
  sleep 0.5
done
if [[ -z "$window_id" ]]; then
  echo 'Atenea SSH window was not visible within 30 seconds' >&2
  exit 1
fi

# Let the packaged WebView finish loading before capturing this synthetic UI.
sleep 3
import -window "$window_id" "$capture"
test -s "$capture"
echo "Captured Atenea SSH window: $capture"
