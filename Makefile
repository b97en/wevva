# Settings in .env (KEY=value lines, unquoted) are exported to `make run` and friends.
-include .env
export

RUST_DIR := rust/retrieve-weather

.PHONY: all build test lint run dry-run history clean

all: build

## build: compile bin/wevva and bin/retrieve-weather
build:
	go build -o bin/wevva ./cmd/wevva
	cargo --quiet build --release --manifest-path $(RUST_DIR)/Cargo.toml
	install -D -m 0755 $(RUST_DIR)/target/release/retrieve-weather bin/retrieve-weather

## test: run the Go and Rust tests
test:
	go test -race ./...
	cargo --quiet test --manifest-path $(RUST_DIR)/Cargo.toml

## lint: check formatting and run the linters
lint:
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed on:"; gofmt -l .; exit 1; }
	go vet ./...
	cargo --quiet fmt --manifest-path $(RUST_DIR)/Cargo.toml --check
	cargo --quiet clippy --manifest-path $(RUST_DIR)/Cargo.toml --all-targets -- -D warnings

## run: build, then run the agent
run: build
	./bin/wevva

## dry-run: build, then run one cycle without touching the thermostat
dry-run: build
	./bin/wevva -once -dry-run

## history: show the last 24 hours that wevva recorded
history: build
	./bin/wevva -history 24h

clean:
	rm -rf bin
	cargo --quiet clean --manifest-path $(RUST_DIR)/Cargo.toml
