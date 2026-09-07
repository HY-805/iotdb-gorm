# Migration Guide

## Import path and fixed versions

Use the maintained repository module:

```text
github.com/HY-805/iotdb-gorm
```

The compatibility baseline is Go `1.23.2`, GORM `v1.23.4`, TreeModel query client `iotdb-client-go v1.3.7`, and Tablet/TableModel client `iotdb-client-go/v2 v2.0.8`.

## From the upstream prototype

1. Replace the old `github.com/wkk778/gorm-iotdb` module path with `github.com/HY-805/iotdb-gorm`.
2. Keep the `gorm.Open(gormiotdb.Open(dsn), ...)` call shape, or use structured `gormiotdb.New(gormiotdb.Config{...})` configuration.
3. Set `gorm.Config.SkipDefaultTransaction: true`; IoTDB does not expose the relational transaction semantics required by GORM's default callbacks.
4. Use `iotdb:"time"` for the timestamp and, in TreeModel, use `iotdb:"device"` or the configured table path for device routing.
5. In TableModel, mark `TAG`, `ATTRIBUTE`, and `FIELD` explicitly with `iotdb` tags.
6. Replace any code relying on implicit transaction, `Updates`, `Save`, `Delete`, `Preload`, `Join`, index, or destructive migration behavior with an explicit supported API or IoTDB statement.

## Platform rollout

Start with an isolated IoTDB adapter and keep the existing TDengine path unchanged. Pin a release tag instead of `main`. Rollback is a dependency-version revert; no TDengine schema or business path is modified by this package.

## Validation status

Local tests, race tests, vet, and Tablet conversion benchmarks are available. Real IoTDB 1.3.1 TreeModel and IoTDB 2.0.8 TableModel validation passed on 2026-09-07; IoTDB 2.0.10 TableModel validation remains pending until its test environment is available.
