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
the generated no-match branch to re-check the raw input: an exact empty object
(``{}``, the input the generated empty-struct check just nilled out) leaves the
wrapper unset instead of erroring; anything else keeps the strict oneOf error,
so invalid filter values (``true``, ``{"$eq":42}``, …) are still rejected
instead of being silently discarded.

It also rewrites the generated ``MarshalJSON`` fallback, which returns
``(nil, nil)`` for an unset wrapper — ``encoding/json`` rejects a nil slice
with ``unexpected end of JSON input``, so a decoded ``{}`` filter could not be
marshaled back into a request. The fallback now returns the ``null`` literal,
the same wire value ``omitempty``-less unset fields produce.

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

_UNSET_BRANCH = """\
} else { // no match
\t\t// `{}` is a valid instance of the advanced-filter variant (all of its
\t\t// properties are optional), but the empty-struct check above nils the
\t\t// match out again. Accept exactly that case as an unset wrapper; any
\t\t// other no-match input is genuinely invalid and keeps the oneOf error
\t\t// (orchestration-cluster-api-go#78).
\t\tif string(bytes.TrimSpace(data)) == "{}" {
\t\t\treturn nil
\t\t}
\t\treturn fmt.Errorf("data failed to match schemas in oneOf(%s)")
\t}
}"""

_NIL_MARSHAL = re.compile(
    r"\treturn nil, nil // no data in oneOf schemas\n"
)

_NULL_MARSHAL = (
    "\t// An unset wrapper marshals to `null`: returning a nil slice here makes\n"
    '\t// encoding/json fail with "unexpected end of JSON input"\n'
    "\t// (orchestration-cluster-api-go#78).\n"
    '\treturn []byte("null"), nil // no data in oneOf schemas\n'
)


def _snake(name: str) -> str:
    return re.sub(r"(?<!^)(?=[A-Z])", "_", name).lower()


def _ensure_bytes_import(text: str) -> str:
    if '"bytes"' in text:
        return text
    return text.replace('import (\n\t"encoding/json"', 'import (\n\t"bytes"\n\t"encoding/json"', 1)


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
            lambda m: _UNSET_BRANCH % m.group("name") if m.group("name") == name else m.group(0),
            text,
        )
        if count != 1:
            raise RuntimeError(
                f"expected exactly one oneOf no-match branch in {path.name}, found {count}"
            )
        new_text, marshal_count = _NIL_MARSHAL.subn(_NULL_MARSHAL, new_text)
        if marshal_count != 1:
            raise RuntimeError(
                f"expected exactly one nil-marshal fallback in {path.name}, found {marshal_count}"
            )
        if new_text != text:
            new_text = _ensure_bytes_import(new_text)
            path.write_text(new_text, encoding="utf-8")
            patched += 1
    print(f"    patched {patched} filter-property union(s) to accept an empty object")
