"""Hook 04 — remove openapi-generator scaffolding.

The Go generator emits non-library scaffolding alongside the client package
(per-operation docs, test stubs, a standalone README, a git-push script, CI
config, and the re-serialized spec). None of it belongs in the committed SDK, so
this hook deletes it, leaving only the compilable client sources.
"""
from __future__ import annotations

import shutil
from pathlib import Path

_DIRS = ["test", "docs", "api", ".openapi-generator"]
_FILES = [
    "git_push.sh",
    ".travis.yml",
    ".gitignore",
    ".openapi-generator-ignore",
    "README.md",
]


def run(ctx) -> None:
    client_dir: Path = ctx["client_dir"]
    removed = 0
    for d in _DIRS:
        p = client_dir / d
        if p.is_dir():
            shutil.rmtree(p)
            removed += 1
    for f in _FILES:
        p = client_dir / f
        if p.is_file():
            p.unlink()
            removed += 1
    print(f"    removed {removed} scaffolding item(s) from client/")
