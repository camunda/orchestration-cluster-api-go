#!/usr/bin/env bash
#
# extract-spec.sh — Extract the Camunda OpenAPI spec from a local camunda/camunda checkout.
#
# Usage:
#   ./scripts/extract-spec.sh <camunda-repo-path> <branch-or-tag> <output-dir>
#
# Examples:
#   ./scripts/extract-spec.sh /path/to/camunda/camunda origin/stable/8.7 spec/8.7
#   ./scripts/extract-spec.sh /path/to/camunda/camunda origin/stable/8.8 spec/8.8
#   ./scripts/extract-spec.sh /path/to/camunda/camunda origin/stable/8.9 spec/8.9
#   ./scripts/extract-spec.sh /path/to/camunda/camunda origin/main       spec/main
#
# For 8.7 and 8.8 the spec is a single monolithic rest-api.yaml.
# For 8.9+ (and main) the spec is split into multiple files under a v2/ subdirectory
# and needs to be bundled with Redocly CLI.
set -euo pipefail

CAMUNDA_REPO="${1:?Usage: $0 <camunda-repo-path> <branch-or-tag> <output-dir>}"
REF="${2:?Usage: $0 <camunda-repo-path> <branch-or-tag> <output-dir>}"
OUTPUT_DIR="${3:?Usage: $0 <camunda-repo-path> <branch-or-tag> <output-dir>}"

SPEC_BASE="zeebe/gateway-protocol/src/main/proto"

mkdir -p "$OUTPUT_DIR"

# Detect spec layout: check if the v2/ subdirectory has files on this ref.
if git -C "$CAMUNDA_REPO" ls-tree --name-only "$REF" "${SPEC_BASE}/v2/" 2>/dev/null | grep -q .; then
    echo "[$REF] Multi-file spec detected (v2/ subdirectory). Extracting and bundling..."

    # Create a temp dir for the raw v2 files
    TMPDIR=$(mktemp -d)
    trap "rm -rf $TMPDIR" EXIT

    # Extract all v2/*.yaml files
    git -C "$CAMUNDA_REPO" ls-tree --name-only "$REF" "${SPEC_BASE}/v2/" | while read -r filepath; do
        filename=$(basename "$filepath")
        git -C "$CAMUNDA_REPO" show "${REF}:${filepath}" > "${TMPDIR}/${filename}"
    done

    echo "  Extracted $(ls "$TMPDIR"/*.yaml 2>/dev/null | wc -l | tr -d ' ') YAML files"

    # Bundle with Redocly CLI
    echo "  Bundling with Redocly CLI..."
    npx --yes @redocly/cli@2.25.3 bundle "${TMPDIR}/rest-api.yaml" -o "${OUTPUT_DIR}/bundled-api.yaml" 2>&1 | grep -v "EBADENGINE\|Warning:" || true

    echo "  Bundled spec: ${OUTPUT_DIR}/bundled-api.yaml ($(wc -l < "${OUTPUT_DIR}/bundled-api.yaml") lines)"
else
    echo "[$REF] Monolithic spec detected. Extracting..."

    # Just extract the single file
    git -C "$CAMUNDA_REPO" show "${REF}:${SPEC_BASE}/rest-api.yaml" > "${OUTPUT_DIR}/bundled-api.yaml"

    echo "  Spec: ${OUTPUT_DIR}/bundled-api.yaml ($(wc -l < "${OUTPUT_DIR}/bundled-api.yaml") lines)"
fi

echo "Done."
