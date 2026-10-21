VERSION ?= 0.1.0-fase1
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

.PHONY: build test fmt-check vuln validate-example fixtures compose-check

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/scanner ./cmd/scanner
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/api ./cmd/api

# Las pruebas de integración levantan PostgreSQL 16 embebido (o usan ARSOP_TEST_PG_DSN).
test:
	go vet ./...
	go test -race -count=1 ./...

fmt-check:
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...

validate-example: build
	bin/scanner config validate --file config/scanner.example.yaml

fixtures:
	python3 -I tests/fixtures/synthetic/make_fixtures.py tests/fixtures/synthetic/tree

compose-check:
	cd deploy && docker compose config -q
