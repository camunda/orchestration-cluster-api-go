#!/usr/bin/env bash
#
# fetch-proto.sh — Fetch the Zeebe gateway.proto from camunda/camunda, pinned to
# $SPEC_REF, into external-spec/proto/. This proto defines StreamActivatedJobs and
# the job-action RPCs used by the gRPC streaming job worker.
#
# Usage:
#   ./scripts/fetch-proto.sh
#   SPEC_REF=stable/8.8 ./scripts/fetch-proto.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

SPEC_REF="${SPEC_REF:-main}"
PROTO_PATH="zeebe/gateway-protocol/src/main/proto/gateway.proto"
RAW_URL="https://raw.githubusercontent.com/camunda/camunda/${SPEC_REF}/${PROTO_PATH}"
DEST="external-spec/proto/gateway.proto"

mkdir -p external-spec/proto

echo "==> Fetching gateway.proto (ref: ${SPEC_REF})..."
echo "    $RAW_URL"
# A read that dies mid-response is raw.githubusercontent.com having a bad moment,
# not a wrong ref, so it must not fail the build on the first attempt.
curl -fsSL --retry 5 --retry-all-errors --retry-delay 3 \
  --connect-timeout 10 --max-time 60 "$RAW_URL" -o "$DEST"

# Sanity check: the file must contain the streaming RPC we depend on.
if ! grep -q "StreamActivatedJobs" "$DEST"; then
  echo "ERROR: fetched proto does not contain StreamActivatedJobs — wrong ref or moved path?" >&2
  exit 1
fi

echo "==> gateway.proto written to $DEST"
