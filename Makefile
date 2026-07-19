.PHONY: help install-tools bundle fetch-proto generate build test test-race lint vet fmt fmt-check tidy tidy-check examples sync-readme sync-readme-check coverage check clean

SPEC_REF ?= main
GO ?= go

help:
	@echo "Camunda Orchestration Cluster API — Go SDK"
	@echo ""
	@echo "Targets:"
	@echo "  make bundle        Re-bundle the upstream OpenAPI spec (ref: $(SPEC_REF)) via camunda-schema-bundler"
	@echo "  make fetch-proto   Fetch the Zeebe gateway.proto (ref: $(SPEC_REF)) from camunda/camunda"
	@echo "  make generate      Regenerate the REST client + gRPC stubs + run post-processing"
	@echo "  make build         Build all packages"
	@echo "  make test          Run all tests"
	@echo "  make test-race     Run all tests with the race detector"
	@echo "  make vet           go vet"
	@echo "  make lint          golangci-lint (if installed) + buf lint"
	@echo "  make fmt           gofmt -w ."
	@echo "  make fmt-check     Fail if gofmt would change any file"
	@echo "  make tidy          go mod tidy"
	@echo "  make tidy-check    Fail if go.mod or go.sum is not tidy"
	@echo "  make examples      Build the example programs (README snippet sources)"
	@echo "  make sync-readme   Inject example snippets into README.md"
	@echo "  make coverage      Verify all 198 operations have an example (operation-map.json)"
	@echo "  make check         Full local CI gate"
	@echo "  make clean         Remove build artifacts"

install-tools:
	$(GO) install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

# Re-bundle the upstream OpenAPI spec, then regenerate everything.
bundle:
	./scripts/bundle-spec.sh
	$(MAKE) generate

# Fetch the gateway.proto from upstream (pinned to $SPEC_REF).
fetch-proto:
	./scripts/fetch-proto.sh

# Regenerate the REST client (openapi-generator), the gRPC stubs (buf), then run
# the post-processing hooks (Domain Type System, semantic fields, facade).
generate:
	./scripts/generate.sh

build:
	$(GO) build ./...

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -w .

fmt-check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed on:"; echo "$$out"; exit 1; fi

tidy:
	$(GO) mod tidy

tidy-check:
	$(GO) mod tidy -diff

lint: vet
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run || echo "golangci-lint not installed; ran go vet only"
	npx --yes @bufbuild/buf@1.72.0 lint || true

examples:
	$(GO) build ./examples/...

sync-readme:
	python3 scripts/sync-readme-snippets.py

sync-readme-check:
	python3 scripts/sync-readme-snippets.py --check

coverage:
	python3 scripts/check-example-coverage.py

check: fmt-check tidy-check vet build test examples sync-readme-check coverage

clean:
	rm -rf dist
	$(GO) clean ./...
