# Benchmark

## Current State

The repository has local conversion benchmarks and no committed network benchmark result yet. The local benchmarks measure GORM-to-Tablet conversion, not IoTDB server throughput.

| Date | Go | OS | IoTDB target | Benchmark | Status |
| --- | --- | --- | --- | --- | --- |
| 2026-09-07 | 1.23.2 | macOS arm64 | 1.3.1 TreeModel | CRUD integration; Tablet conversion, 1/100/1000/5000 rows | CRUD passed; throughput pending |
| 2026-09-07 | 1.23.2 | CI runner | 2.0.10 TableModel | Tablet conversion, 1/100/1000/5000 rows | server result pending |

## Workflow

- `scripts/update-benchmark.sh` is the CI entrypoint.
- The nightly workflow is defined in `.github/workflows/nightly-benchmark.yml`.
- `BenchmarkCreateBatchTablet` compares GORM batch conversion with direct official Tablet construction.
- IoTDB 1.3.1 correctness passed, but network throughput and query latency remain unmeasured. IoTDB 2.0.10 still needs a reachable TableModel environment; local benchmark success is not server acceptance.
