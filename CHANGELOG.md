# Changelog

## [Unreleased]

- fixed the module path to `github.com/HY-805/iotdb-gorm` and the Go 1.23.2 compatibility baseline
- added default TreeModel support for IoTDB 1.3.1 and explicit TableModel support for IoTDB 2.0.10
- routed GORM batch creates through the official Tablet and RelationalTablet APIs
- added non-destructive model-specific migrators, lifecycle closing, explicit unsupported-operation errors, and opt-in integration tests
- recorded the current IoTDB 1.3.1 login-802 block and deferred the final dual-version compatibility claim until live validation completes

## [0.1.0] - 2026-03-26

- bootstrapped the imported upstream standalone module snapshot
- migrated the original `gormiotdb/dialector.go` entrypoints into `dialector/` and a root re-export
- added a reusable IoTDB `database/sql` wrapper in `driver/`
- added a best-effort IoTDB migrator, tag-based shard routing, and dry-run-safe tests
- added example programs, docs, CI scaffolding, docker-compose, and Makefile targets
