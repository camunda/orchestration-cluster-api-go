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

Two relaxation modes:
  * VERSION_SKEW_OPTIONAL — relaxed in every model. Use for globally-unique
    field names that are spec-ahead-of-server.
  * MODEL_SCOPED_OPTIONAL — relaxed only in the named model file. Use for
    common field names (e.g. `resource`, `form`) that are legitimately required
    on other models but are mutually-exclusive union members on this one.

Extend either as further spec-ahead-of-server / union-shaped fields are found (a
broader audit is advisable per issue #3).
"""
from __future__ import annotations

import re
from pathlib import Path

# TEMPORARY: fields that are required in the spec but not yet emitted by shipped
# servers. Each entry is a stopgap — REMOVE it once the target server version
# emits the field (or the spec is corrected), so the required-presence check is
# restored. Confirmed against live clusters; revisit per issue #3's broader audit.
VERSION_SKEW_OPTIONAL = [
    # TEMPORARY (issue #3): never emitted by 8.9 / 8.10 servers.
    "physicalTenantId",
    # TEMPORARY: not emitted by 8.10.0-alpha3 (ActivatedJobResult); present on 8.10
    # once the server catches up — drop this entry then. Blocks the job worker.
    "leaseToken",
]

# Fields relaxed only within a specific model file. DeploymentMetadataResult
# declares processDefinition/decisionDefinition/decisionRequirements/form/resource
# all required, but a deployment response populates exactly the members matching
# the deployed resource kinds (e.g. only processDefinition for a BPMN); the server
# omits the rest, so the strict all-required check rejects valid responses. The
# field names collide with genuinely-required fields on other models, so they must
# be scoped here rather than in VERSION_SKEW_OPTIONAL.
MODEL_SCOPED_OPTIONAL = {
    "model_deployment_metadata_result.go": [
        "processDefinition",
        "decisionDefinition",
        "decisionRequirements",
        "form",
        "resource",
    ],
}


def run(ctx) -> None:
    client_dir: Path = ctx["client_dir"]
    # Match a bare slice element like `\t\t\t"physicalTenantId",` — this is only
    # ever a requiredProperties entry; field tags and setters use other shapes.
    global_patterns = [re.compile(r'^\s*"' + re.escape(f) + r'",\s*$') for f in VERSION_SKEW_OPTIONAL]

    total = 0
    files = 0
    for f in sorted(client_dir.glob("model_*.go")):
        patterns = list(global_patterns)
        for scoped in MODEL_SCOPED_OPTIONAL.get(f.name, ()):
            patterns.append(re.compile(r'^\s*"' + re.escape(scoped) + r'",\s*$'))

        lines = f.read_text(encoding="utf-8").splitlines(keepends=True)
        kept = [ln for ln in lines if not any(p.match(ln) for p in patterns)]
        removed = len(lines) - len(kept)
        if removed:
            f.write_text("".join(kept), encoding="utf-8")
            total += removed
            files += 1
    print(f"    relaxed {total} version-skew required-field check(s) across {files} file(s)")
