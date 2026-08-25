# Atenea benchmarks

This directory contains reproducible evidence for tests and performance runs.
The canonical structured result is benchmarks/runs/latest/summary.json; raw Go
test events and benchmark output live beside it so a report can be audited
without trusting a hand-edited table.

The `coverage.out` a run leaves in the same directory is deliberately not
committed. It is the intermediate the coverage figures in summary.json are
computed from, nothing reads it afterwards, and every run overwrites all
858 KB of it.

Run the fast profile with:

    go run ./cmd/atenea-benchmark --profile quick --benchmark-runs 3

Qualification and stress profiles use at least ten independent processes:

    go run ./cmd/atenea-benchmark --profile qualification --benchmark-runs 10
    go run ./cmd/atenea-benchmark --profile stress --benchmark-runs 10

The current reference environment is recorded from the host. On the
qualification machine it is expected to read **MacBook Air M5, 24 GB,
darwin/arm64**. The collector deliberately ignores serial numbers, UUIDs and
other hardware identity fields.

## Status semantics

- 🟢 GREEN: valid evidence, all tests pass and the target coverage is met.
- 🟠 ORANGE: valid evidence with skips, limited samples or a known
  comparability limitation.
- 🔴 RED: a test failed, coverage is below the floor, or the benchmark did
  not produce valid evidence.

Compare only results with the same benchmark, profile, dataset and compatible
hardware. A result on a shared CI runner is not silently mixed with the
MacBook baseline.
