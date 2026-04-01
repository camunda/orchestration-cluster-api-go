.PHONY: spec generate build clean tidy spec-all generate-all build-all compare help

GOPATH_BIN      := $(shell GOTOOLCHAIN=go1.25.8 go env GOPATH)/bin
CAMUNDA_REPO    ?= /Users/amanyadav/camunda/camunda
CAMUNDA_VERSION ?= main
VERSIONS        := 8.7 8.8 8.9 main

# Map a version label to a git ref
ref = $(if $(filter main,$(1)),origin/main,origin/stable/$(1))
# Map a version label to a Go package suffix (no dots allowed)
pkg_suffix = $(subst .,,$(1))

# ---------------------------------------------------------------------------
# Per-version targets (override CAMUNDA_VERSION as needed)
# ---------------------------------------------------------------------------

# Extract OpenAPI spec for CAMUNDA_VERSION from local repo
spec:
	@./scripts/extract-spec.sh "$(CAMUNDA_REPO)" "$(call ref,$(CAMUNDA_VERSION))" "spec/$(CAMUNDA_VERSION)"

# Generate Go client for CAMUNDA_VERSION
generate: spec
	@echo "==> Generating Go client for $(CAMUNDA_VERSION)..."
	@mkdir -p pkg/camunda/$(CAMUNDA_VERSION)
	GOTOOLCHAIN=go1.25.8 $(GOPATH_BIN)/oapi-codegen \
		-package camunda$(call pkg_suffix,$(CAMUNDA_VERSION)) \
		-generate models,client \
		-o pkg/camunda/$(CAMUNDA_VERSION)/client.gen.go \
		spec/$(CAMUNDA_VERSION)/bundled-api.yaml
	@echo "    Applying post-generation fixes..."
	@python3 scripts/fix-generated.py pkg/camunda/$(CAMUNDA_VERSION)/client.gen.go
	@echo "    Done: pkg/camunda/$(CAMUNDA_VERSION)/client.gen.go"

# ---------------------------------------------------------------------------
# Batch targets — run across all VERSIONS
# ---------------------------------------------------------------------------

spec-all:
	@for v in $(VERSIONS); do $(MAKE) --no-print-directory spec CAMUNDA_VERSION=$$v; echo; done

generate-all:
	@for v in $(VERSIONS); do $(MAKE) --no-print-directory generate CAMUNDA_VERSION=$$v; echo; done

build-all: tidy
	GOTOOLCHAIN=go1.25.8 go build ./...

# ---------------------------------------------------------------------------
# Comparison
# ---------------------------------------------------------------------------

compare:
	@echo ""
	@echo "=== Camunda Go Client — Version Comparison ==="
	@echo ""
	@printf "%-10s  %12s  %10s  %10s\n" "Version" "Spec Lines" "Go Lines" "Go Types"
	@printf "%-10s  %12s  %10s  %10s\n" "-------" "----------" "--------" "--------"
	@for v in $(VERSIONS); do \
		spec_lines=$$(wc -l < spec/$$v/bundled-api.yaml 2>/dev/null | tr -d ' ' || echo "N/A"); \
		go_lines=$$(wc -l < pkg/camunda/$$v/client.gen.go 2>/dev/null | tr -d ' ' || echo "N/A"); \
		go_types=$$(grep -c '^type ' pkg/camunda/$$v/client.gen.go 2>/dev/null || echo "N/A"); \
		printf "%-10s  %12s  %10s  %10s\n" "$$v" "$$spec_lines" "$$go_lines" "$$go_types"; \
	done
	@echo ""

# ---------------------------------------------------------------------------
# Build / utility
# ---------------------------------------------------------------------------

build:
	GOTOOLCHAIN=go1.25.8 go build ./...

tidy:
	GOTOOLCHAIN=go1.25.8 go mod tidy

clean:
	rm -rf pkg/camunda/8.*/
	rm -rf pkg/camunda/main/
	rm -rf spec/8.*/
	rm -rf spec/main/
	rm -f spec/bundled-api.yaml
	rm -f pkg/camunda/client.gen.go

help:
	@echo "Usage:"
	@echo "  make spec              CAMUNDA_VERSION=8.8   # Extract spec for one version"
	@echo "  make generate          CAMUNDA_VERSION=8.8   # Generate client for one version"
	@echo "  make spec-all                                # Extract specs for all versions"
	@echo "  make generate-all                            # Generate clients for all versions"
	@echo "  make build-all                               # Tidy + build everything"
	@echo "  make compare                                 # Compare generated clients"
	@echo "  make clean                                   # Remove generated files"
	@echo ""
	@echo "Versions: $(VERSIONS)"
	@echo "Camunda repo: $(CAMUNDA_REPO)"
