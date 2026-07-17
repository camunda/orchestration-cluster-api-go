#!/usr/bin/env python3
"""
Verify that every operation in the bundled OpenAPI spec has an example mapped in
examples/operation-map.json, and that every mapped region actually exists in an
examples/*.go file.

This is the CI gate that keeps per-endpoint example coverage complete.

Usage:
    python3 scripts/check-example-coverage.py
"""

from __future__ import annotations

import json
import re
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
SPEC_METADATA = REPO_ROOT / "external-spec" / "bundled" / "spec-metadata.json"
OPERATION_MAP = REPO_ROOT / "examples" / "operation-map.json"
EXAMPLES_DIR = REPO_ROOT / "examples"

_REGION_RE = re.compile(r"^\s*//\s*region\s+([\w.-]+)\s*$")


def main() -> None:
    metadata = json.loads(SPEC_METADATA.read_text(encoding="utf-8"))
    op_ids = [op["operationId"] for op in metadata.get("operations", [])]

    operation_map = json.loads(OPERATION_MAP.read_text(encoding="utf-8"))

    # Index every region defined across the example files.
    region_file: dict[str, str] = {}
    for go_file in sorted(EXAMPLES_DIR.glob("*.go")):
        for line in go_file.read_text(encoding="utf-8").splitlines():
            m = _REGION_RE.match(line.strip())
            if m:
                region_file[m.group(1)] = go_file.name

    errors: list[str] = []

    # 1. Every spec operation must be mapped.
    missing = [op for op in op_ids if op not in operation_map]
    for op in missing:
        errors.append(f"operation '{op}' has no entry in operation-map.json")

    # 2. Every mapped region must resolve to a real region in the named file.
    for op_id, entries in operation_map.items():
        for entry in entries:
            region = entry.get("region", "")
            file = entry.get("file", "")
            if region not in region_file:
                errors.append(f"operation '{op_id}' references missing region '{region}'")
            elif file and region_file[region] != file:
                errors.append(
                    f"operation '{op_id}' region '{region}' is in "
                    f"{region_file[region]}, not {file}"
                )

    if errors:
        print("Example coverage check failed:", file=sys.stderr)
        for e in errors:
            print(f"  - {e}", file=sys.stderr)
        print(
            f"\n{len(missing)} of {len(op_ids)} operations are unmapped. "
            "Add an example region and an operation-map.json entry.",
            file=sys.stderr,
        )
        sys.exit(1)

    print(f"Example coverage OK: all {len(op_ids)} operations mapped to existing regions.")


if __name__ == "__main__":
    main()
