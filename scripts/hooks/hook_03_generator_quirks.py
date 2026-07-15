"""Hook 03 — openapi-generator output quirks.

The Go generator occasionally dereferences a required *array* parameter as if it
were a pointer (e.g. ``len(*r.files)`` / ``range *r.files`` where ``files`` is
declared ``[]*os.File``). Dereferencing a slice does not compile.

A slice-typed request field is never a pointer, so any ``*r.<field>`` on such a
field is unambiguously a generator bug. This hook rewrites those occurrences to
``r.<field>`` in every ``api_*.go`` file.
"""
from __future__ import annotations

import re
from pathlib import Path

# Request-struct field declared as a slice, e.g. "\tfiles []*os.File".
_SLICE_FIELD = re.compile(r"^\t(\w+)\s+\[\]", re.MULTILINE)


def run(ctx) -> None:
    client_dir: Path = ctx["client_dir"]
    total = 0
    for f in sorted(client_dir.glob("api_*.go")):
        text = f.read_text(encoding="utf-8")
        slice_fields = set(_SLICE_FIELD.findall(text))
        if not slice_fields:
            continue
        new_text = text
        for name in slice_fields:
            # Deref of a slice field is always a bug; collapse "*r.<name>" -> "r.<name>".
            new_text = new_text.replace(f"*r.{name}", f"r.{name}")
        if new_text != text:
            f.write_text(new_text, encoding="utf-8")
            total += 1
    print(f"    patched slice-deref quirks in {total} api file(s)")
