"""Hook 10 — forward-compatible response decoding (issue #124).

openapi-generator's ``go`` model template emits ``decoder.DisallowUnknownFields()``
in every model's ``UnmarshalJSON``. A server that adds a field to a response
(a non-breaking API change) then breaks every call returning that model with
``json: unknown field "..."``.

This hook removes the strict-unknown-field check from every model reachable
(transitively) from an operation response in the bundled spec. Required-field
presence checks and type validation are untouched; unknown fields are simply
ignored. Request-only models keep strict decoding — they are never decoded from
server output, and the strictness disambiguates the undiscriminated request-side
filter unions.

Response-reachable unions are all discriminated (or scalar key unions), so
relaxing their member models does not create ambiguous matches; this hook
fails generation if that invariant ever breaks.
"""
from __future__ import annotations

import json
import re
from pathlib import Path

_STRICT = re.compile(r"^[ \t]*decoder\.DisallowUnknownFields\(\)\n", re.MULTILINE)
_VAR = re.compile(r"var\w+ := _(\w+)\{\}")


def _refs(node, acc: set[str]) -> None:
    if isinstance(node, dict):
        for k, v in node.items():
            if k == "$ref" and isinstance(v, str):
                acc.add(v.rsplit("/", 1)[-1])
            else:
                _refs(v, acc)
    elif isinstance(node, list):
        for v in node:
            _refs(v, acc)


def response_schemas(spec: dict) -> set[str]:
    comps = spec.get("components", {})
    schemas = comps.get("schemas", {})
    responses = comps.get("responses", {})
    stack: set[str] = set()
    for item in spec.get("paths", {}).values():
        for op in item.values():
            if isinstance(op, dict) and "responses" in op:
                _refs(op["responses"], stack)
    seen: set[str] = set()
    todo = list(stack)
    while todo:
        name = todo.pop()
        if name in seen:
            continue
        seen.add(name)
        node = schemas.get(name, responses.get(name))
        if node is not None:
            nxt: set[str] = set()
            _refs(node, nxt)
            todo.extend(nxt)
    for name in seen:
        node = schemas.get(name)
        if isinstance(node, dict) and "oneOf" in node and "discriminator" not in node:
            members = []
            _refs(node["oneOf"], members_set := set())
            members = [m for m in members_set if schemas.get(m, {}).get("type") == "object" or "properties" in schemas.get(m, {})]
            if len(members) > 1:
                raise RuntimeError(
                    f"hook_10: response union {name} has multiple undiscriminated object members "
                    f"{sorted(members)}; lenient decoding could make it ambiguous"
                )
    return {n for n in seen if n in schemas}


def run(ctx) -> None:
    spec = json.loads(Path(ctx["spec_path"]).read_text(encoding="utf-8"))
    names = response_schemas(spec)
    changed = 0
    for path in sorted(Path(ctx["client_dir"]).glob("model_*.go")):
        text = path.read_text(encoding="utf-8")
        if "DisallowUnknownFields" not in text:
            continue
        m = _VAR.search(text)
        if not m or m.group(1) not in names:
            continue
        new = _STRICT.sub("", text)
        if new != text:
            path.write_text(new, encoding="utf-8")
            changed += 1
    print(f"  relaxed unknown-field decoding in {changed} response models")
