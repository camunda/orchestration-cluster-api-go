#!/usr/bin/env bash
#
# bundle-spec.sh — Fetch and bundle the upstream Camunda OpenAPI spec via
# camunda-schema-bundler into external-spec/bundled/.
#
# Usage:
#   ./scripts/bundle-spec.sh          # bundle spec at ref $SPEC_REF (default: main)
#   SPEC_REF=stable/8.8 ./scripts/bundle-spec.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

SPEC_REF="${SPEC_REF:-main}"
OUT_SPEC="external-spec/bundled/rest-api.bundle.json"
OUT_META="external-spec/bundled/spec-metadata.json"

mkdir -p external-spec/bundled

echo "==> Bundling upstream OpenAPI spec (ref: ${SPEC_REF}) via camunda-schema-bundler..."
npx --yes camunda-schema-bundler@^2.4.3 --ref "$SPEC_REF" \
  --output-spec "$OUT_SPEC" \
  --output-metadata "$OUT_META"

echo "==> Bundled spec written to $OUT_SPEC"
