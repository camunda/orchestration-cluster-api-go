"""Hook 90 — ergonomic facade generation.

Delegates to ``cmd/facadegen``, which AST-parses the generated client and emits
``facade_generated.go`` (one ergonomic method per REST operation on
``*CamundaClient``).

The facade references the hand-written client wiring (``c.raw`` and
``c.wrapError``). Until that wiring exists in the root package, this hook skips
generation so the module stays buildable — the facade is produced once the client
facade layer lands.
"""
from __future__ import annotations

import subprocess
from pathlib import Path


def _has_camunda_client(root: Path) -> bool:
    for p in root.glob("*.go"):
        try:
            if "CamundaClient struct" in p.read_text(encoding="utf-8"):
                return True
        except OSError:
            continue
    return False


def run(ctx) -> None:
    root: Path = ctx["root"]
    if not _has_camunda_client(root):
        print("    skipping facade: root CamundaClient wiring not present yet")
        return
    metadata_path = str(ctx.get("metadata_path", ""))
    subprocess.run(
        ["go", "run", "./cmd/facadegen", "client", "facade_generated.go", metadata_path, "examples"],
        cwd=str(root),
        check=True,
    )
    print("    generated facade_generated.go")
