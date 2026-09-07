#!/usr/bin/env bash
set -euo pipefail

go test ./dialector -run '^$' -bench . -benchmem | tee /tmp/iotdb-gorm-benchmark.txt
echo "Benchmark output is printed above; commit measured values to docs/benchmark.md after reviewing the runner and workload."
