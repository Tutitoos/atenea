#!/usr/bin/env bash
set -euo pipefail

coverage_file=coverage.out
if [[ $# -gt 0 ]]; then
	coverage_file=$1
fi
floor=${ATENEA_COVERAGE_FLOOR:-77.0}
target=${ATENEA_COVERAGE_TARGET:-80.0}
summary_file=coverage-summary.txt
if [[ -n "${ATENEA_COVERAGE_SUMMARY:-}" ]]; then
	summary_file=$ATENEA_COVERAGE_SUMMARY
fi
test -f "$coverage_file" || {
	echo "coverage profile not found: $coverage_file" >&2
	exit 1
}

total="$(go tool cover -func="$coverage_file" | awk '/^total:/ { gsub("%", "", $3); print $3 }')"
test -n "$total" || {
	echo "coverage profile has no total: $coverage_file" >&2
	exit 1
}

printf 'coverage_schema=1\ncoverage_total=%s\ncoverage_floor=%s\ncoverage_target=%s\ncoverage_commit=%s\ncoverage_run_id=%s\ncoverage_go_version=%s\ncoverage_platform=%s\n' \
	"$total" \
	"$floor" \
	"$target" \
	"${GITHUB_SHA:-local}" \
	"${GITHUB_RUN_ID:-local}" \
	"${ATENEA_GO_VERSION:-$(go version)}" \
	"${ATENEA_COVERAGE_PLATFORM:-$(go env GOOS)/$(go env GOARCH)}" > "$summary_file"

awk -v total="$total" -v floor="$floor" -v target="$target" 'BEGIN {
	if (total < floor) {
		printf "coverage %.1f%% is below the %.1f%% floor\n", total, floor
		exit 1
	}
	if (total < target) {
		printf "coverage %.1f%% is below the %.1f%% target\n", total, target
		exit 1
	}
	printf "coverage %.1f%% meets the staged %.1f%% target\n", total, target
}'
