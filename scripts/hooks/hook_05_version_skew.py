"""Hook 05 — version-skew tolerance.

openapi-generator emits a strict UnmarshalJSON that hard-fails when a
spec-required property is absent from the raw JSON. The bundled spec runs ahead
of shipped servers, so some spec-required fields are never emitted by any running
cluster (e.g. `physicalTenantId` on ActivatedJobResult — see issue #3), which
makes otherwise-valid responses fail to decode.

This hook removes such fields from the generated `requiredProperties` presence
checks so the responses decode. The fields remain on the struct and simply take
their zero value when the server omits them. Only the required-*presence* check
is relaxed; nothing else about the model changes.

Extend VERSION_SKEW_OPTIONAL as further spec-ahead-of-server fields are found (a
broader audit is advisable per issue #3).
"""
from __future__ import annotations

import re
from pathlib import Path

# Fields that are required in the spec but not emitted by shipped servers.
VERSION_SKEW_OPTIONAL = [
    # issue #3: required in the spec, never emitted by 8.9 / 8.10 servers.
    "physicalTenantId",
]


def run(ctx) -> None:
    client_dir: Path = ctx["client_dir"]
    # Match a bare slice element like `\t\t\t"physicalTenantId",` — this is only
    # ever a requiredProperties entry; field tags and setters use other shapes.
    patterns = [re.compile(r'^\s*"' + re.escape(f) + r'",\s*$') for f in VERSION_SKEW_OPTIONAL]

    total = 0
    files = 0
    for f in sorted(client_dir.glob("model_*.go")):
        lines = f.read_text(encoding="utf-8").splitlines(keepends=True)
        kept = [ln for ln in lines if not any(p.match(ln) for p in patterns)]
        removed = len(lines) - len(kept)
        if removed:
            f.write_text("".join(kept), encoding="utf-8")
            total += removed
            files += 1
    print(f"    relaxed {total} version-skew required-field check(s) across {files} file(s)")
