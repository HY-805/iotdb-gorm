#!/usr/bin/env bash
set -euo pipefail

expected_go="1.23.2"
expected_gorm="v1.23.4"
expected_iotdb="v2.0.8"

actual_go="$(go env GOVERSION)"
actual_go="${actual_go#go}"
actual_gorm="$(go list -m -f '{{.Version}}' gorm.io/gorm)"
actual_iotdb="$(go list -m -f '{{.Version}}' github.com/apache/iotdb-client-go/v2)"

test "$actual_go" = "$expected_go" || {
  echo "unsupported Go version: expected $expected_go, got $actual_go" >&2
  exit 1
}
test "$actual_gorm" = "$expected_gorm" || {
  echo "unsupported GORM version: expected $expected_gorm, got $actual_gorm" >&2
  exit 1
}
test "$actual_iotdb" = "$expected_iotdb" || {
  echo "unsupported IoTDB client version: expected $expected_iotdb, got $actual_iotdb" >&2
  exit 1
}

echo "dependency baseline: Go $actual_go, GORM $actual_gorm, IoTDB client $actual_iotdb"
