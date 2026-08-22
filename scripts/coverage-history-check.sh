#!/usr/bin/env bash
set -euo pipefail

coverage_file=coverage.out
if [[ $# -gt 0 ]]; then
	coverage_file=$1
fi

if [[ "${GITHUB_ACTIONS:-}" != "true" ]]; then
	echo "coverage history skipped outside GitHub Actions"
	exit 0
fi

for command_name in curl jq unzip; do
	command -v "$command_name" >/dev/null 2>&1 || {
		echo "coverage history requires $command_name" >&2
		exit 1
	}
done

: "${GITHUB_TOKEN:?GITHUB_TOKEN is required in GitHub Actions}"
: "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required in GitHub Actions}"
: "${GITHUB_RUN_ID:?GITHUB_RUN_ID is required in GitHub Actions}"

current="$(go tool cover -func="$coverage_file" | awk '/^total:/ { gsub("%", "", $3); print $3 }')"
test -n "$current" || {
	echo "coverage profile has no total: $coverage_file" >&2
	exit 1
}

api_base="https://api.github.com/repos/$GITHUB_REPOSITORY"
workflow_file=${ATENEA_COVERAGE_WORKFLOW:-ci.yml}
history_branch=${ATENEA_COVERAGE_HISTORY_BRANCH:-main}
artifact_name=${ATENEA_COVERAGE_ARTIFACT:-atenea-coverage-history}
listing="$(mktemp)"
artifacts="$(mktemp)"
archive="$(mktemp)"
extract="$(mktemp -d)"
trap 'rm -f "$listing" "$artifacts" "$archive"; rm -rf "$extract"' EXIT

auth_header=(
	-H "Accept: application/vnd.github+json"
	-H "Authorization: Bearer $GITHUB_TOKEN"
)
runs_url="$api_base/actions/workflows/$workflow_file/runs?branch=$history_branch&status=success&per_page=100"
curl -fsSL "${auth_header[@]}" "$runs_url" >"$listing"

previous_run="$(jq -r --arg current "$GITHUB_RUN_ID" --arg branch "$history_branch" '
	[.workflow_runs[]
	 | select((.id | tostring) != $current)
	 | select(.head_branch == $branch)]
	| sort_by(.created_at) | reverse | .[0].id // empty
' "$listing")"
if [[ -z "$previous_run" ]]; then
	echo "coverage history has no previous successful $history_branch run; establishing baseline"
	exit 0
fi

artifacts_url="$api_base/actions/runs/$previous_run/artifacts?per_page=100"
curl -fsSL "${auth_header[@]}" "$artifacts_url" >"$artifacts"
artifact_url="$(jq -r --arg name "$artifact_name" '
	[.artifacts[]
	 | select(.expired == false)
	 | select(.name == $name)]
	| sort_by(.created_at) | reverse | .[0].archive_download_url // empty
' "$artifacts")"
if [[ -z "$artifact_url" ]]; then
	echo "coverage history run $previous_run has no $artifact_name artifact; establishing baseline"
	exit 0
fi

curl -fsSL "${auth_header[@]}" "$artifact_url" -o "$archive"
unzip -p "$archive" 'coverage-summary.txt' >"$extract/coverage-summary.txt"

schema="$(awk -F= '$1 == "coverage_schema" { print $2 }' "$extract/coverage-summary.txt")"
previous="$(awk -F= '$1 == "coverage_total" { print $2 }' "$extract/coverage-summary.txt")"
test "$schema" = 1 || {
	echo "coverage history artifact has unsupported schema: ${schema:-missing}" >&2
	exit 1
}
test -n "$previous" || {
	echo "coverage history artifact has no total" >&2
	exit 1
}

awk -v current="$current" -v previous="$previous" 'BEGIN {
	if (current < previous - 1.0) {
		printf "coverage regressed from %.1f%% to %.1f%%\n", previous, current
		exit 1
	}
	printf "coverage history: previous %.1f%%, current %.1f%%\n", previous, current
}'
