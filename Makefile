.PHONY: build fmt-check lint lint-fix test ci

build:
	go build -o bin/jev ./cmd/jev

fmt-check:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }

lint:
	golangci-lint run

lint-fix:
	golangci-lint run --fix

test:
	go test -race -count=1 ./...

ci: fmt-check lint test
