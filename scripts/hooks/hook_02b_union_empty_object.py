"""Hook 02b — filter-property unions accept an empty object.

Every ``*FilterProperty`` oneOf union pairs an exact-value branch with an
advanced-filter branch whose properties are all optional, so ``{}`` is a valid
instance of the advanced-filter variant (the JS / Python / C# / PHP SDKs accept
and send it — see orchestration-cluster-api-go#78).

openapi-generator's oneOf ``UnmarshalJSON`` nils out a branch that round-trips
to ``{}`` ("empty struct") and then, when no branch matched, errors with
``data failed to match schemas in oneOf(...)`` — so ``{"filter":{"name":{}}}``
fails to decode even though it is spec-valid.

For each filter-property union named in the spec metadata this hook rewrites
the generated no-match branch to leave the wrapper unset (all branches nil —
``GetActualInstance()`` returns nil and ``MarshalJSON`` yields ``null``, the
same shape as a never-populated wrapper) instead of returning an error.

Unions whose branches all have required properties (e.g.
``ProcessInstanceCreationInstruction``, ``AuthorizationRequest``) keep the
strict error: for them ``{}`` genuinely matches no schema. The rewrite is
therefore driven by the ``*FilterProperty`` unions listed in the spec metadata,
not by the mere presence of the generated error string.
"""
from __future__ import annotations

import re
from pathlib import Path

_NO_MATCH = re.compile(
    r"\} else \{ // no match\n"
    r'\t\treturn fmt\.Errorf\("data failed to match schemas in oneOf\((?P<name>\w+)\)"\)\n'
    r"\t\}\n"
    r"\}"
)

_UNSET_BRANCH = (
    "} else { // no match\n"
    "\t\t// `{}` is a valid instance of the advanced-filter variant (all of its\n"
    "\t\t// properties are optional), but the empty-struct check above nils the\n"
    "\t\t// match out again. Treat a no-match decode as an unset wrapper rather\n"
    "\t\t// than an error (orchestration-cluster-api-go#78).\n"
    "\t\treturn nil\n"
    "\t}\n"
    "}"
)


def _snake(name: str) -> str:
    return re.sub(r"(?<!^)(?=[A-Z])", "_", name).lower()


def run(ctx) -> None:
    client_dir: Path = ctx["client_dir"]
    filter_unions = {
        u["name"]
        for u in ctx["metadata"].get("unions", [])
        if u.get("name", "").endswith("FilterProperty")
    }
    if not filter_unions:
        raise RuntimeError("spec metadata lists no *FilterProperty unions; refusing to patch nothing")

    patched = 0
    for name in sorted(filter_unions):
        path = client_dir / f"model_{_snake(name)}.go"
        if not path.exists():
            raise RuntimeError(f"filter union {name} has no generated model file {path.name}")
        text = path.read_text(encoding="utf-8")
        new_text, count = _NO_MATCH.subn(
            lambda m: _UNSET_BRANCH if m.group("name") == name else m.group(0), text
        )
        if count != 1:
            raise RuntimeError(
                f"expected exactly one oneOf no-match branch in {path.name}, found {count}"
            )
        if new_text != text:
            path.write_text(new_text, encoding="utf-8")
            patched += 1
    print(f"    patched {patched} filter-property union(s) to accept an empty object")
