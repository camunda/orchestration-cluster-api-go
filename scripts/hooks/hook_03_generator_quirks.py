"""Hook 03 — openapi-generator output quirks.

Two independent generator bugs are patched here.

1. Slice deref. The Go generator occasionally dereferences a required *array*
   parameter as if it were a pointer (e.g. ``len(*r.files)`` / ``range *r.files``
   where ``files`` is declared ``[]*os.File``). Dereferencing a slice does not
   compile. A slice-typed request field is never a pointer, so any ``*r.<field>``
   on such a field is unambiguously a generator bug; those occurrences are
   rewritten to ``r.<field>``.

2. Inclusive-bound error text. For an *inclusive* ``minimum``/``maximum`` the
   generator emits the correct guard but an off-by-one message: ``minimum: 1``
   yields ``if r.backupId < 1 { reportError("backupId must be greater than 1") }``,
   which tells the caller that the smallest legal value is illegal. The guard is
   right, so only the message is rewritten — to "greater than or equal to" /
   "less than or equal to". Exclusive bounds (``<=`` / ``>=`` guards) already read
   correctly and are left alone.
"""
from __future__ import annotations

import re
from pathlib import Path

# Request-struct field declared as a slice, e.g. "\tfiles []*os.File".
_SLICE_FIELD = re.compile(r"^\t(\w+)\s+\[\]", re.MULTILINE)

# An inclusive-minimum guard and the message that contradicts it. The guard line
# and the reportError line are adjacent, and the field name and bound must match
# on both, so this cannot touch an exclusive bound (guarded with "<=").
_INCLUSIVE_MIN_MESSAGE = re.compile(
    r'(?P<head>if r\.(?P<field>\w+) < (?P<bound>-?[\d.]+) \{\n'
    r'[^\n]*reportError\("(?P=field) must be greater than )(?P=bound)"\)'
)
_INCLUSIVE_MAX_MESSAGE = re.compile(
    r'(?P<head>if r\.(?P<field>\w+) > (?P<bound>-?[\d.]+) \{\n'
    r'[^\n]*reportError\("(?P=field) must be less than )(?P=bound)"\)'
)


def run(ctx) -> None:
    client_dir: Path = ctx["client_dir"]
    deref_files = 0
    bound_messages = 0
    for f in sorted(client_dir.glob("api_*.go")):
        text = f.read_text(encoding="utf-8")
        new_text = text

        for name in set(_SLICE_FIELD.findall(text)):
            # Deref of a slice field is always a bug; collapse "*r.<name>" -> "r.<name>".
            new_text = new_text.replace(f"*r.{name}", f"r.{name}")
        if new_text != text:
            deref_files += 1

        for pattern in (_INCLUSIVE_MIN_MESSAGE, _INCLUSIVE_MAX_MESSAGE):
            new_text, count = pattern.subn(
                lambda m: f'{m.group("head")}or equal to {m.group("bound")}")', new_text
            )
            bound_messages += count

        if new_text != text:
            f.write_text(new_text, encoding="utf-8")
    print(
        f"    patched slice-deref quirks in {deref_files} api file(s); "
        f"corrected {bound_messages} inclusive-bound message(s)"
    )
