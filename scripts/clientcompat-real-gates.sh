#!/usr/bin/env bash
set -euo pipefail

# Opt-in only. The caller supplies an actual installed client binary and a
# batch command that drives it through MCP and workflow.status. This gate never
# writes a persistent client profile and never treats a discovered binary as a
# successful connection. The transcript is the acceptance artifact.
clients=(codex claude opencode chatgpt omp)
root=${ATENEA_TEST_REAL_ROOT:?set ATENEA_TEST_REAL_ROOT to a disposable directory}
seconds=${ATENEA_TEST_REAL_MAX_SECONDS:-90}
mkdir -p "$root"
failed=0

for client in "${clients[@]}"; do
	upper=$(printf '%s' "$client" | tr '[:lower:]' '[:upper:]')
	opt="ATENEA_TEST_REAL_${upper}"
	if [[ "${!opt:-}" != "1" ]]; then
		echo "SKIP ${client}: set ${opt}=1 to opt in"
		continue
	fi
	if [[ "$client" == "chatgpt" || "$client" == "omp" ]]; then
		echo "PARTIAL ${client}: manual client validation is required; no persistent configuration was changed"
		failed=1
		continue
	fi
	bin_var="ATENEA_TEST_REAL_${upper}_BIN"
	args_var="ATENEA_TEST_REAL_${upper}_ARGS"
	transcript_var="ATENEA_TEST_REAL_${upper}_TRANSCRIPT"
	bin=${!bin_var:?set ${bin_var} to the real client executable}
	args_text=${!args_var:-}
	transcript_name=${!transcript_var:?set ${transcript_var} to a relative transcript filename}
	if [[ "$transcript_name" == /* || "$transcript_name" == *"/"* || "$transcript_name" == *".."* || "$transcript_name" == *$'\n'* ]]; then
		echo "${transcript_var} must be one relative filename inside the sandbox" >&2
		failed=1
		continue
	fi
	if [[ ! -x "$bin" ]]; then
		echo "${bin_var} is not executable" >&2
		failed=1
		exit 2
	fi
	atenea_bin=${ATENEA_TEST_REAL_ATENEA_BIN:?set ATENEA_TEST_REAL_ATENEA_BIN to the absolute atenea executable}
	if [[ ! -x "$atenea_bin" ]]; then
		echo "ATENEA_TEST_REAL_ATENEA_BIN is not executable" >&2
		failed=1
		exit 2
	fi
	atenea_bin=$(python3 - "$atenea_bin" <<'PY'
import os, sys
print(os.path.realpath(sys.argv[1]))
PY
)
	sandbox=$(mktemp -d "$root/${client}.XXXXXX")
	mkdir -p "$sandbox/home" "$sandbox/codex" "$sandbox/config" "$sandbox/state" "$sandbox/data"
	core_socket=${ATENEA_TEST_REAL_CORE_SOCKET:?set ATENEA_TEST_REAL_CORE_SOCKET to the running Atenea core socket}
	if [[ ! -S "$core_socket" ]]; then
		echo "ATENEA_TEST_REAL_CORE_SOCKET is not a live Unix socket" >&2
		failed=1
		exit 2
	fi
	mkdir -p "$sandbox/state/atenea/run"
	ln -s "$core_socket" "$sandbox/state/atenea/run/core.sock"
	transcript="$sandbox/$transcript_name"
	sentinel=${ATENEA_TEST_REAL_SENTINEL:?set ATENEA_TEST_REAL_SENTINEL to a pre-existing external sentinel}
	before=$(shasum -a 256 "$sentinel" | awk '{print $1}')
	tree_hash() { tar -cf - "$1" 2>/dev/null | shasum -a 256 | awk '{print $1}'; }
	protected_before=""
	if [[ -n "${ATENEA_TEST_REAL_PROTECTED_ROOTS:-}" ]]; then
		IFS=: read -r -a protected <<< "$ATENEA_TEST_REAL_PROTECTED_ROOTS"
		for path in "${protected[@]}"; do protected_before+="$(tree_hash "$path")\n"; done
	fi
	run_id="real-${client}-$(date -u +%Y%m%dT%H%M%SZ)-$$"
	workflow_id="workflow-${client}-$$"
	read -r -a args <<< "$args_text"
	overlay_json="$sandbox/overlay.json"
	if ! "$atenea_bin" compat-overlay --client "$client" >"$overlay_json"; then
		echo "${client} overlay generation failed" >&2
		failed=1
		continue
	fi
	case "$client" in
		codex|claude)
			if ! python3 - "$overlay_json" >"$sandbox/overlay.args" <<'PY'
import json, sys
data = json.load(open(sys.argv[1]))
for value in data.get("args", []):
    print(value)
PY
			then
				echo "${client} overlay was not valid JSON" >&2
				failed=1
				continue
			fi
			while IFS= read -r value; do args+=("$value"); done <"$sandbox/overlay.args"
			;;
		opencode)
			if ! OPENCODE_CONFIG_CONTENT=$(python3 - "$overlay_json" <<'PY'
import json, sys
data = json.load(open(sys.argv[1]))
print(data.get("env", {}).get("OPENCODE_CONFIG_CONTENT", ""), end="")
PY
			); then
				echo "opencode overlay was not valid JSON" >&2
				failed=1
				continue
			fi
			export OPENCODE_CONFIG_CONTENT
			;;
	esac
	launcher="$sandbox/launch.py"
	cat >"$launcher" <<'PY'
import os
import sys

allowed = {
    key: os.environ[key]
    for key in (
        "PATH", "HOME", "CODEX_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME",
        "XDG_DATA_HOME", "TMPDIR", "LANG", "TERM", "ATENEA_PILOT_RUN_ID",
        "ATENEA_PILOT_WORKFLOW_ID", "OPENCODE_CONFIG_CONTENT",
    )
    if key in os.environ
}
os.setsid()
os.execve(sys.argv[1], sys.argv[1:], allowed)
PY
	mkdir -p "$sandbox/tmp"
	launch_env=(
		"PATH=${ATENEA_TEST_REAL_PATH:-/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin}"
		"HOME=$sandbox/home"
		"CODEX_HOME=$sandbox/codex"
		"XDG_CONFIG_HOME=$sandbox/config"
		"XDG_STATE_HOME=$sandbox/state"
		"XDG_DATA_HOME=$sandbox/data"
		"TMPDIR=$sandbox/tmp"
		"LANG=${LANG:-C.UTF-8}"
		"TERM=${TERM:-dumb}"
		"ATENEA_PILOT_RUN_ID=$run_id"
		"ATENEA_PILOT_WORKFLOW_ID=$workflow_id"
	)
	if [[ "$client" == "opencode" ]]; then
		launch_env+=("OPENCODE_CONFIG_CONTENT=$OPENCODE_CONFIG_CONTENT")
	fi
	python_bin=$(command -v python3) || {
		echo "python3 is required" >&2
		exit 2
	}
	set +e
	env -i "${launch_env[@]}" "$python_bin" "$launcher" "$bin" "${args[@]}" >"$transcript" 2>&1 &
	pid=$!
	start=$(date +%s)
	timed_out=0
	while kill -0 "$pid" 2>/dev/null; do
		if (( $(date +%s) - start >= seconds )); then
			timed_out=1
			kill -TERM -- "-$pid" 2>/dev/null
			break
		fi
		sleep 1
	done
	# The client may exit while a helper remains in its session, or ignore
	# SIGTERM itself. Never wait for either without a deadline: terminate the
	# whole isolated process group, allow a short grace period, then force it.
	kill -TERM -- "-$pid" 2>/dev/null
	grace_start=$(date +%s)
	while kill -0 -- "-$pid" 2>/dev/null && (( $(date +%s) - grace_start < 2 )); do
		sleep 0.1
	done
	if kill -0 -- "-$pid" 2>/dev/null; then
		kill -KILL -- "-$pid" 2>/dev/null
	fi
	wait "$pid" 2>/dev/null
	status=$?
	if (( timed_out )); then
		status=124
		echo "${client} exceeded ${seconds}s" >&2
	fi
	group_gone=0
	for _ in 1 2 3 4 5 6 7 8 9 10; do
		if ! kill -0 -- "-$pid" 2>/dev/null; then
			group_gone=1
			break
		fi
		sleep 0.1
	done
	set -e
	if [[ ! -f "$transcript" ]]; then
		printf '{"client":"%s","status":"unknown","reason":"sandbox transcript was not created"}\n' "$client"
		failed=1
		continue
	fi
	if (( ! group_gone )); then
		printf '{"client":"%s","status":"unknown","reason":"client process group survived forced termination"}\n' "$client"
		failed=1
		continue
	fi
	# Process completion and text matching are only collection signals. They
	# cannot certify MCP correlation, protocol version, or presentation. A
	# controlled recorder/verifier and an independent observer must consume the
	# transcript before any matrix entry can become real/pass.
	after=$(shasum -a 256 "$sentinel" | awk '{print $1}')
	if [[ "$before" != "$after" ]]; then
		printf '{"client":"%s","status":"unknown","reason":"protected sentinel changed"}\n' "$client"
		failed=1
		continue
	fi
	if [[ -n "${ATENEA_TEST_REAL_PROTECTED_ROOTS:-}" ]]; then
		protected_after=""
		for path in "${protected[@]}"; do protected_after+="$(tree_hash "$path")\n"; done
		if [[ "$protected_before" != "$protected_after" ]]; then
			printf '{"client":"%s","status":"unknown","reason":"protected root changed"}\n' "$client"
			failed=1
			continue
		fi
	fi
	if (( status == 0 )); then
		printf '{"client":"%s","status":"candidate","source":"real","reason":"process and transcript collected; structured recorder verifier and external observer still required"}\n' "$client"
	else
		printf '{"client":"%s","status":"unknown","reason":"real client exited before controlled verification","exit_status":%d}\n' "$client" "$status"
		failed=1
	fi
done

exit "$failed"
