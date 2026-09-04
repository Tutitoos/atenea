#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
command -v omp >/dev/null || { echo "OMP must be installed for the real integration gate" >&2; exit 1; }
expected="omp/18.0.11"
found="$(omp --version)"
if [[ "$found" != "$expected" ]]; then
  echo "OMP version mismatch: expected $expected, found $found" >&2
  exit 1
fi
echo "$found"
ATENEA_TEST_REAL_OMP=1 go test -race -p 1 -count=1 ./cmd/atenea -run '^(TestTheSkeletonBeatsThroughTheRealAdapter|TestTaskRunsAgainstARealRepositoryEndToEnd)$'
