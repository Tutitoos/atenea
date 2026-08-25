#!/usr/bin/env bash
set -euo pipefail

# These are deliberately generous regression barriers rather than claims about
# a specific CPU. They catch accidental disk/network work in hot paths while
# remaining stable across the Linux and macOS CI runners.
run_benchmark() {
	local package="$1" benchmark="$2" max_ns="$3" max_bytes="$4"
	local output line ns bytes
	output="$(go test "$package" -run '^$' -bench "$benchmark" -benchmem -benchtime=200ms)"
	line="$(awk -v name="$benchmark" '$1 ~ ("^" name "-") { print; exit }' <<<"$output")"
	test -n "$line" || {
		echo "benchmark $benchmark did not produce a result" >&2
		exit 1
	}
	ns="$(awk '{ for (i = 1; i <= NF; i++) if ($i == "ns/op") print $(i - 1) }' <<<"$line")"
	bytes="$(awk '{ for (i = 1; i <= NF; i++) if ($i == "B/op") print $(i - 1) }' <<<"$line")"
	# Without this the gate passes on a line it could not read. An empty ns or
	# bytes reaches awk as the empty string, every `>` comparison against a
	# number is then false, and the barrier reports success having measured
	# nothing -- which is the one failure mode a barrier must not have. The
	# load gate has always checked this; this one did not.
	test -n "$ns" && test -n "$bytes" || {
		echo "benchmark $benchmark reported no ns/op or B/op" >&2
		echo "$line" >&2
		exit 1
	}
	awk -v name="$benchmark" -v ns="$ns" -v max_ns="$max_ns" -v bytes="$bytes" -v max_bytes="$max_bytes" 'BEGIN {
		if (ns > max_ns) {
			printf "%s: %.0f ns/op exceeds %.0f\n", name, ns, max_ns
			exit 1
		}
		if (bytes > max_bytes) {
			printf "%s: %.0f B/op exceeds %.0f\n", name, bytes, max_bytes
			exit 1
		}
		printf "%s: %.0f ns/op, %.0f B/op\n", name, ns, bytes
	}'
}

run_benchmark ./internal/selector BenchmarkSelectMediumCatalog 5000000 200000
run_benchmark ./internal/metrics BenchmarkRecord 5000000 200000
run_benchmark ./pkg/contract BenchmarkPlanLayersMediumDAG 5000000 500000
