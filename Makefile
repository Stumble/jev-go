COVERAGE_MIN ?= 85.0

.PHONY: build fmt-check lint lint-fix test coverage ci

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

coverage:
	go test -covermode=atomic -coverprofile=coverage.out ./...
	@total="$$(go tool cover -func=coverage.out | awk '/^total:/ { gsub("%", "", $$3); print $$3 }')"; \
		echo "Total coverage: $${total}% (minimum $(COVERAGE_MIN)%)"; \
		awk -v total="$${total}" -v minimum="$(COVERAGE_MIN)" 'BEGIN { exit(total + 0 < minimum + 0) }'

ci: fmt-check lint test coverage
