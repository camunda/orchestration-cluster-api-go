#!/usr/bin/env python3
"""Post-processing orchestrator for the generated Go SDK surfaces.

Runs the numbered hooks in scripts/hooks/ in lexicographic order. Each hook is a
module exposing ``run(ctx)`` where ``ctx`` carries the resolved paths and the
parsed spec metadata. Hooks fix openapi-generator output that the generator does
not produce correctly — chiefly the Camunda Domain Type System — and emit the
ergonomic facade.

Usage:
    python3 scripts/postprocess.py \
        --client-dir client \
        --spec external-spec/bundled/rest-api.bundle.json \
        --metadata external-spec/bundled/spec-metadata.json
"""
from __future__ import annotations

import argparse
import importlib.util
import json
from pathlib import Path


def _load_hook(path: Path):
    spec = importlib.util.spec_from_file_location(path.stem, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load hook {path}")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--client-dir", required=True, help="generated REST client dir")
    ap.add_argument("--spec", required=True, help="bundled OpenAPI spec path")
    ap.add_argument("--metadata", required=True, help="spec metadata path")
    args = ap.parse_args()

    root = Path(__file__).resolve().parent.parent
    metadata_path = (root / args.metadata).resolve() if not Path(args.metadata).is_absolute() else Path(args.metadata)
    metadata = json.loads(Path(args.metadata).read_text(encoding="utf-8"))
    ctx = {
        "root": root,
        "client_dir": (root / args.client_dir).resolve(),
        "spec_path": (root / args.spec).resolve() if not Path(args.spec).is_absolute() else Path(args.spec),
        "metadata": metadata,
        "metadata_path": metadata_path,
    }

    hooks_dir = Path(__file__).resolve().parent / "hooks"
    hook_files = sorted(hooks_dir.glob("hook_*.py"))
    if not hook_files:
        print("no hooks found in scripts/hooks/")
        return
    for hook_path in hook_files:
        mod = _load_hook(hook_path)
        if not hasattr(mod, "run"):
            continue
        print(f"==> {hook_path.name}")
        mod.run(ctx)


if __name__ == "__main__":
    main()
