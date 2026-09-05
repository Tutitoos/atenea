#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
# Every remaining mention must be an immutable record or a negative guard.
while IFS= read -r path; do
 case "$path" in
  CHANGELOG.md|README.md|pkg/contract/version.go|internal/metrics/migrations/0005_raw_failure.sql|scripts/kivgraph-integration-smoke.mjs) ;;
  benchmarks/*|docs/data/benchmarks/latest.json) ;;
  docs/content/architecture.md|docs/content/benchmarks/*|docs/content/diagnosing-providers.md|docs/content/measuring-the-wrong-process.md|docs/content/not-built-yet.md|docs/content/v1-final-audit.md|docs/content/v1-readiness.md|docs/content/migration-4.md) ;;
  internal/config/config.go|internal/config/config_test.go|internal/config/retired_serena_test.go|cmd/atenea/headroom_wrap.go|cmd/atenea/main.go|cmd/atenea/retired_serena_test.go|tools/mcp-agree|tools/test_mcp_agree.py|scripts/provider-matrix-check.sh|scripts/serena-retirement-check.sh|scripts/v1-readiness.sh) ;;
  *) echo "unclassified Serena reference: $path" >&2; exit 1 ;;
 esac
done < <(git grep -Il -i serena || test "$?" -eq 1)
test ! -d internal/adapter/serena
echo "retired Serena references classified"
