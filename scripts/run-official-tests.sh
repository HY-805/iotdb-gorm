#!/usr/bin/env bash
set -euo pipefail

echo "running adapter and opt-in integration tests"
go test ./...
