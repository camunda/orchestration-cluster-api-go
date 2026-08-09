#!/usr/bin/env bash
#
# generate.sh — Regenerate the SDK's generated surfaces from the fetched inputs.
#
# Pipeline:
#   1. (if missing) bundle the OpenAPI spec and fetch gateway.proto
#   2. openapi-generator  → client/  (REST client)
#   3. buf generate       → pb/       (gRPC stubs)
#   4. postprocess.py     → Domain Type System, semantic fields, generator fixes,
#                            and the ergonomic facade (facade_generated.go)
#   5. gofmt + go build   → verify the generated code compiles
#
# Usage:
#   ./scripts/generate.sh            # generate from existing inputs (fetch if absent)
#   ./scripts/generate.sh --bundle   # re-bundle the OpenAPI spec first
#   ./scripts/generate.sh --proto    # re-fetch gateway.proto first
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

BUNDLED_SPEC="external-spec/bundled/rest-api.bundle.json"
BUNDLED_META="external-spec/bundled/spec-metadata.json"
PROTO="external-spec/proto/gateway.proto"

for arg in "$@"; do
  case "$arg" in
    --bundle) ./scripts/bundle-spec.sh ;;
    --proto) ./scripts/fetch-proto.sh ;;
    *) echo "unknown flag: $arg" >&2; exit 2 ;;
  esac
done

[[ -f "$BUNDLED_SPEC" ]] || ./scripts/bundle-spec.sh
[[ -f "$PROTO" ]] || ./scripts/fetch-proto.sh

echo "==> Generating REST client with openapi-generator..."
npx --yes @openapitools/openapi-generator-cli generate -c openapi-generator-config.yaml

echo "==> Generating gRPC stubs with buf..."
npx --yes @bufbuild/buf@1.72.0 generate

echo "==> Post-processing: Domain Type System + semantic fields + facade..."
python3 scripts/postprocess.py \
  --client-dir client \
  --spec "$BUNDLED_SPEC" \
  --metadata "$BUNDLED_META"

echo "==> Formatting generated code..."
# gofmt is not idempotent over the generated API doc comments: a single pass leaves some
# files unformatted, which then surfaces as phantom generation drift in CI. Run to a fixed
# point and fail loudly if it does not converge — never silence this step.
GOFMT_TARGETS=(client pb facade_generated.go consistency_generated.go)
unformatted=""
for attempt in 1 2 3; do
  gofmt -w "${GOFMT_TARGETS[@]}"
  unformatted="$(gofmt -l "${GOFMT_TARGETS[@]}")"
  if [ -z "$unformatted" ]; then
    break
  fi
done
if [ -n "$unformatted" ]; then
  printf 'error: gofmt did not converge after %s passes:\n%s\n' "$attempt" "$unformatted" >&2
  exit 1
fi

echo "==> Verifying the module builds..."
go build ./...

echo "==> Done."
