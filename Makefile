.PHONY: build test lint fmt check

# Until cmd/deployctl exists (M1), this just compiles every package.
build:
	go build ./...

test:
	go test -race ./...

lint:
	go vet ./...
	@if command -v golangci-lint >/dev/null; then golangci-lint run; \
	else echo "golangci-lint not installed, ran go vet only"; fi

fmt:
	gofmt -w .

# What CI runs. Run it before pushing.
check:
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)
	go vet ./...
	go test -race ./...
