#!/usr/bin/env python3
"""Normalize the bundled spec into the openapi-generator input.

Upstream types a date that can be cleared with an empty string as a reference to an
``x-clearable`` schema (``ClearableDateTime``: an ``anyOf`` of a ``date-time`` and a
``maxLength: 0`` string), usually wrapped in a single-element ``allOf`` so the
property can carry its own description. openapi-generator turns that into a
``ClearableDateTime`` union type, losing ``time.Time``. ``x-clearable`` asks SDKs
to keep the native date and add an explicit clear instead, so this step rewrites
every such property back to a plain nullable ``date-time``::

    {"type": "string", "format": "date-time", "nullable": true, "x-clearable": true}

keeping the property's own description (or the clearable schema's, if it has
none). The ``x-clearable`` marker left on the property is what hook 09 uses to add
the ``Clear<Field>`` / ``Is<Field>Cleared`` pair. The ``x-clearable`` schemas are
then dropped, so no unused union type is generated.

A reference to an ``x-clearable`` schema anywhere other than a schema property,
or an ``x-clearable`` schema that is not a date-time, fails the run rather than
generating a type the hooks cannot make clearable.

Usage:
    python3 scripts/normalize-spec.py <bundled-spec.json> <generator-input.json>
"""
from __future__ import annotations

import json
import sys
from pathlib import Path

_PREFIX = "#/components/schemas/"


def _is_date_time(schema: dict) -> bool:
    if schema.get("format") == "date-time":
        return True
    return any(b.get("format") == "date-time" for b in schema.get("anyOf") or schema.get("oneOf") or [])


def _clearable_ref(prop: dict, clearable: set[str]) -> str | None:
    """The x-clearable schema a property is typed by: a $ref or a one-element allOf."""
    ref = prop.get("$ref")
    if ref is None and isinstance(prop.get("allOf"), list) and len(prop["allOf"]) == 1:
        ref = prop["allOf"][0].get("$ref")
    if isinstance(ref, str) and ref.startswith(_PREFIX) and ref[len(_PREFIX):] in clearable:
        return ref[len(_PREFIX):]
    return None


def _rewrite_properties(node, schemas: dict, clearable: set[str], where: str) -> list[str]:
    rewritten: list[str] = []
    if isinstance(node, dict):
        props = node.get("properties")
        if isinstance(props, dict):
            for name, prop in props.items():
                target = _clearable_ref(prop, clearable) if isinstance(prop, dict) else None
                if target is None:
                    continue
                source = schemas[target]
                new = {"type": "string", "format": "date-time"}
                description = prop.get("description") or source.get("description")
                if description:
                    new["description"] = description
                new["nullable"] = bool(source.get("nullable"))
                new.update({k: v for k, v in prop.items() if k not in ("$ref", "allOf", "description")})
                new["x-clearable"] = True
                props[name] = new
                rewritten.append(f"{where}.{name}")
        for key, value in node.items():
            rewritten += _rewrite_properties(value, schemas, clearable, f"{where}/{key}")
    elif isinstance(node, list):
        for i, value in enumerate(node):
            rewritten += _rewrite_properties(value, schemas, clearable, f"{where}/{i}")
    return rewritten


def _remaining_refs(node, clearable: set[str], where: str, out: list[str]) -> None:
    if isinstance(node, dict):
        ref = node.get("$ref")
        if isinstance(ref, str) and ref.startswith(_PREFIX) and ref[len(_PREFIX):] in clearable:
            out.append(where)
        for key, value in node.items():
            _remaining_refs(value, clearable, f"{where}/{key}", out)
    elif isinstance(node, list):
        for i, value in enumerate(node):
            _remaining_refs(value, clearable, f"{where}/{i}", out)


def normalize(spec: dict) -> list[str]:
    """Rewrite x-clearable properties in place; return the rewritten property paths."""
    schemas = spec.get("components", {}).get("schemas", {})
    clearable = {name for name, s in schemas.items() if s.get("x-clearable") is True}
    for name in sorted(clearable):
        if not _is_date_time(schemas[name]):
            raise SystemExit(f"normalize-spec: x-clearable schema {name} is not a date-time; "
                             "only clearable dates are supported")
    rewritten: list[str] = []
    for name, schema in schemas.items():
        if name not in clearable:
            rewritten += _rewrite_properties(schema, schemas, clearable, name)
    leftover: list[str] = []
    _remaining_refs(spec, clearable, "#", leftover)
    if leftover:
        raise SystemExit("normalize-spec: x-clearable schema referenced outside a schema property, "
                         "which cannot be made clearable:\n  " + "\n  ".join(leftover))
    for name in clearable:
        del schemas[name]
    return rewritten


def main() -> None:
    if len(sys.argv) != 3:
        raise SystemExit(__doc__)
    src, dst = Path(sys.argv[1]), Path(sys.argv[2])
    spec = json.loads(src.read_text(encoding="utf-8"))
    rewritten = normalize(spec)
    dst.parent.mkdir(parents=True, exist_ok=True)
    dst.write_text(json.dumps(spec, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    print(f"    rewrote {len(rewritten)} x-clearable propert{'y' if len(rewritten) == 1 else 'ies'} as date-time")
    for path in rewritten:
        print(f"      {path}")


if __name__ == "__main__":
    main()
