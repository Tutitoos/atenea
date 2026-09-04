#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
command -v omp >/dev/null || { echo "OMP must be installed for the real integration gate" >&2; exit 1; }
# Print the version as setup evidence. Production call deadlines are unchanged.
omp --version
ATENEA_TEST_REAL_OMP=1 go test -race -p 1 -count=1 ./cmd/atenea -run '^(TestTheSkeletonBeatsThroughTheRealAdapter|TestTaskRunsAgainstARealRepositoryEndToEnd)$'
