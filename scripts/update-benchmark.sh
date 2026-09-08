#!/usr/bin/env bash
set -euo pipefail

go test ./dialector -run '^$' -bench . -benchmem | tee /tmp/iotdb-gorm-benchmark.txt
echo "Benchmark output is printed above; review the runner and workload before recording measured values in release notes."
