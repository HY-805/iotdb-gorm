SHELL := /bin/sh

.PHONY: test lint bench doc release tidy deps

test:
	go test ./...
	go test -race ./...
	go vet ./...

lint:
	golangci-lint run ./...

bench:
	bash ./scripts/update-benchmark.sh

doc:
	go test ./... -run '^$'

release:
	goreleaser release --clean

tidy:
	go mod tidy

deps:
	bash ./scripts/check-dependencies.sh
