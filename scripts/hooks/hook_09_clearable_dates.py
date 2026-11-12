"""Hook 09 — clearable date-time fields.

Some request schemas clear a date attribute with an empty string, while also
declaring it ``format: date-time`` — ``JobResultCorrections`` documents::

    * `dueDate` - clear by providing an empty String

openapi-generator types such a property as ``NullableTime``, which can carry a
date or an explicit null (null *preserves* the value) but never ``""``, so the
clear the API documents cannot be expressed. Changing the field type would break
every caller, so this hook keeps the field and adds a clear marker beside it::

    func (o *JobResultCorrections) ClearDueDate()
    func (o *JobResultCorrections) IsDueDateCleared() bool

``ClearDueDate`` unsets the field and records the clear; ``ToMap`` (and so
``MarshalJSON``) then emits ``"dueDate": ""``. ``SetDueDate``, ``SetDueDateNil``
and ``UnsetDueDate`` drop the marker, so the last call wins.

Decoding needs the reverse: ``NullableTime`` rejects ``""``, so a model with a
clearable date gets an ``UnmarshalJSON`` that takes an empty string for each such
property as a clear (unsetting the date and setting the marker) and otherwise
keeps encoding/json's semantics: it decodes into a copy of the receiver, so absent
properties — clear markers included — are left untouched, and a timestamp or
``null`` for a date replaces its clear, as the setters do.

A model whose generator-emitted ``UnmarshalJSON`` must stay (``Changeset``,
which collects ``additionalProperties``) keeps it, renamed to
``unmarshalGeneratedJSON``; the hook's ``UnmarshalJSON`` takes the clears out of
the input, delegates the rest to it, and then sets the markers. That decoder
replaces the whole receiver, so it also resets clears absent from the input. Any
other generated ``UnmarshalJSON`` fails the run, as the two cannot be merged safely.

The affected properties are read from the spec. A ``date-time`` property is
clearable when any of these holds:

* it is typed by an ``x-clearable`` schema (upstream's ``ClearableDateTime``),
  either directly or through a single-element ``allOf``. ``scripts/normalize-spec.py``
  rewrites such properties to a nullable ``date-time`` carrying ``x-clearable: true``
  before generation, so they stay ``NullableTime``; that property-level marker
  counts too;
* its schema's description has the bullet
  "* `<prop>` - clear by providing an empty String" (or "reset by ...");
* its own description says "Clear by providing an empty String" (or "Reset ...").

The prose forms are what specs before ``x-clearable`` used. A property the spec
stops declaring as a date (and that the generator then types as a string, which
can already hold ``""``) is simply no longer patched. The hook fails loudly
instead of patching nothing: an ``x-clearable`` marker it cannot place on a schema
property, a clearable property whose generated field is not ``NullableTime``, and
a generated anchor this hook rewrites that has moved all fail the run.
"""
from __future__ import annotations

import json
import re
from pathlib import Path

_PREFIX = "#/components/schemas/"
_CLEAR_PHRASE = r"(?:clear|reset) by providing an empty String"
_CLEAR_BULLET = re.compile(rf"\*\s*`(?P<prop>\w+)`\s*-\s*{_CLEAR_PHRASE}", re.IGNORECASE)
_CLEAR_PROSE = re.compile(_CLEAR_PHRASE, re.IGNORECASE)


def _clearable_ref(prop: dict, schemas: dict) -> str | None:
    """The x-clearable schema a property is typed by: a $ref or a one-element allOf."""
    ref = prop.get("$ref")
    if ref is None and isinstance(prop.get("allOf"), list) and len(prop["allOf"]) == 1:
        ref = prop["allOf"][0].get("$ref")
    if isinstance(ref, str) and ref.startswith(_PREFIX):
        name = ref[len(_PREFIX):]
        if (schemas.get(name) or {}).get("x-clearable") is True:
            return name
    return None


def _markers(node, schemas: dict, where: str, out: list[str]) -> None:
    """Every x-clearable property marker or reference to an x-clearable schema."""
    if isinstance(node, dict):
        if node.get("x-clearable") is True or _clearable_ref(node, schemas):
            out.append(where)
            return
        for key, value in node.items():
            _markers(value, schemas, f"{where}/{key}", out)
    elif isinstance(node, list):
        for i, value in enumerate(node):
            _markers(value, schemas, f"{where}/{i}", out)


def _clearable_date_props(spec: dict) -> dict[str, list[str]]:
    schemas = spec.get("components", {}).get("schemas", {})
    out: dict[str, list[str]] = {}
    placed: set[str] = set()
    for name, schema in schemas.items():
        props = schema.get("properties") or {}
        bullets = {m.group("prop") for m in _CLEAR_BULLET.finditer(schema.get("description") or "")}
        for prop_name, prop in props.items():
            marked = prop.get("x-clearable") is True or _clearable_ref(prop, schemas) is not None
            is_date = marked or (prop.get("type") == "string" and prop.get("format") == "date-time")
            documented = prop_name in bullets or _CLEAR_PROSE.search(prop.get("description") or "")
            if is_date and (marked or documented):
                out.setdefault(name, []).append(prop_name)
                placed.add(f"#/components/schemas/{name}/properties/{prop_name}")
    # x-clearable is the generators' contract: a marker this hook cannot place would
    # otherwise generate a date the SDK silently cannot clear.
    found: list[str] = []
    _markers(spec, schemas, "#", found)
    definitions = {f"{_PREFIX}{n}" for n, s in schemas.items() if s.get("x-clearable") is True}
    unplaced = sorted(set(found) - placed - definitions)
    if unplaced:
        raise SystemExit("hook_09: x-clearable used outside a component schema property, "
                         "which this hook cannot make clearable:\n  " + "\n  ".join(unplaced))
    return out


def _replace_once(text: str, old: str, new: str, where: str) -> str:
    if new in text:
        return text  # already patched
    if text.count(old) != 1:
        raise SystemExit(f"hook_09: expected exactly one anchor in {where}:\n{old}")
    return text.replace(old, new, 1)


def _patch(text: str, model: str, prop: str, path: Path) -> tuple[str, str, str]:
    field_re = re.compile(rf"^\t(?P<field>\w+) NullableTime `json:\"{re.escape(prop)},omitempty\"`$", re.MULTILINE)
    m = field_re.search(text)
    if m is None:
        raise SystemExit(
            f"hook_09: {model}.{prop} is clearable but not a NullableTime field in {path.name}; "
            "an x-clearable property must reach openapi-generator as a nullable date-time "
            "(see scripts/normalize-spec.py)"
        )
    field = m.group("field")
    flag = field[0].lower() + field[1:] + "Cleared"
    where = f"{path.name} ({model}.{field})"

    # The marker field, appended to the struct.
    struct_re = re.compile(rf"(type {model} struct \{{\n.*?)(\n\}}\n)", re.DOTALL)
    sm = struct_re.search(text)
    if sm is None:
        raise SystemExit(f"hook_09: struct {model} not found in {path.name}")
    if f"\t{flag} bool" not in sm.group(1):
        text = (
            text[: sm.end(1)]
            + f"\n\t// {flag} is set by Clear{field}: {field} is then sent as \"\", which clears it.\n\t{flag} bool"
            + text[sm.end(1):]
        )

    # Every other way of assigning the field drops the marker: last call wins.
    for setter, body in (
        (f"func (o *{model}) Set{field}(v time.Time) {{\n", f"\to.{field}.Set(&v)\n"),
        (f"func (o *{model}) Set{field}Nil() {{\n", f"\to.{field}.Set(nil)\n"),
        (f"func (o *{model}) Unset{field}() {{\n", f"\to.{field}.Unset()\n"),
    ):
        text = _replace_once(text, setter + body, setter + body + f"\to.{flag} = false\n", where)

    text = _replace_once(
        text,
        f'\tif o.{field}.IsSet() {{\n\t\ttoSerialize["{prop}"] = o.{field}.Get()\n\t}}\n',
        f'\tif o.{flag} && !o.{field}.IsSet() {{\n\t\ttoSerialize["{prop}"] = ""\n'
        f'\t}} else if o.{field}.IsSet() {{\n\t\ttoSerialize["{prop}"] = o.{field}.Get()\n\t}}\n',
        where,
    )

    clear_fn = f"func (o *{model}) Clear{field}() {{"
    if clear_fn not in text:
        text = text.rstrip("\n") + (
            f"\n\n// Clear{field} clears {field}: it is sent as an empty string, which the API\n"
            f"// treats as \"remove the value\" (null or omission instead preserves it).\n"
            f"// Set{field}, Set{field}Nil or Unset{field} afterwards replaces the clear.\n"
            f"{clear_fn}\n\to.{field}.Unset()\n\to.{flag} = true\n}}\n"
            f"\n// Is{field}Cleared reports whether {field} is to be cleared (see Clear{field}).\n"
            f"func (o *{model}) Is{field}Cleared() bool {{\n"
            f"\treturn o != nil && o.{flag} && !o.{field}.IsSet()\n}}\n"
        )
    return text, field, flag


_UNMARSHAL_MARKER = "an empty string for a clearable date"


def _wrap_generated_unmarshal(text: str, model: str, cleared: list[tuple[str, str, str]],
                              names: str, generated: str, path: Path) -> str:
    """Keep the generator's UnmarshalJSON (renamed) and decode the clears around it."""
    inner = f"func (o *{model}) unmarshalGeneratedJSON(data []byte) (err error) {{"
    text = _replace_once(
        text,
        f"\n{generated}",
        f"\n// unmarshalGeneratedJSON is the generator's decoder; UnmarshalJSON wraps it.\n{inner}",
        f"{path.name} ({model}.UnmarshalJSON)",
    )
    lines = [
        f"// UnmarshalJSON decodes a {model} with the generated decoder, taking",
        f"// {_UNMARSHAL_MARKER} ({names}) as a clear: the inverse",
        "// of ToMap. Like the generated decoder, it replaces the whole value, so a clear",
        "// absent from data is reset; a timestamp or null for a date is decoded as usual.",
        f"func (o *{model}) UnmarshalJSON(data []byte) error {{",
        "\tvar raw map[string]json.RawMessage",
        "\tif err := json.Unmarshal(data, &raw); err != nil {",
        "\t\treturn err",
        "\t}",
        "\t// Only a string literal can be a clear; null decodes to \"\" too but preserves.",
        "\tisEmpty := func(v json.RawMessage) bool {",
        "\t\tvar s string",
        "\t\treturn len(v) > 0 && v[0] == '\"' && json.Unmarshal(v, &s) == nil && s == \"\"",
        "\t}",
    ]
    for prop, _, flag in cleared:
        base = flag[: -len("Cleared")]
        lines += [
            f"\t{base}Clear := isEmpty(raw[\"{prop}\"])",
            f"\tif {base}Clear {{",
            f"\t\tdelete(raw, \"{prop}\")",
            "\t}",
        ]
    any_clear = " || ".join(f"{flag[: -len('Cleared')]}Clear" for _, _, flag in cleared)
    lines += [
        f"\tif {any_clear} {{",
        "\t\trest, err := json.Marshal(raw)",
        "\t\tif err != nil {",
        "\t\t\treturn err",
        "\t\t}",
        "\t\tdata = rest",
        "\t}",
        "\tif err := o.unmarshalGeneratedJSON(data); err != nil {",
        "\t\treturn err",
        "\t}",
    ]
    for _, field, flag in cleared:
        base = flag[: -len("Cleared")]
        lines += [
            f"\tif {base}Clear {{",
            f"\t\to.{field}.Unset()",
            f"\t\to.{flag} = true",
            "\t}",
        ]
    lines += ["\treturn nil", "}"]
    return text.rstrip("\n") + "\n\n" + "\n".join(lines) + "\n"


def _unmarshal(text: str, model: str, cleared: list[tuple[str, str, str]], path: Path) -> str:
    """Emit UnmarshalJSON restoring the clear markers; cleared is (prop, field, flag).

    It keeps encoding/json's merge semantics for the model, which has no decoder
    of its own otherwise: it decodes into a copy of the receiver, so a property
    absent from the input (a date's clear marker included) is left untouched.
    """
    signature = f"func (o *{model}) UnmarshalJSON(data []byte) error {{"
    if signature in text:
        if _UNMARSHAL_MARKER not in text:
            raise SystemExit(f"hook_09: {model} in {path.name} already has a generated UnmarshalJSON")
        return text
    names = ", ".join(field for _, field, _ in cleared)
    generated = f"func (o *{model}) UnmarshalJSON(data []byte) (err error) {{"
    if generated in text:
        return _wrap_generated_unmarshal(text, model, cleared, names, generated, path)
    if f"func (o *{model}) UnmarshalJSON(" in text:
        raise SystemExit(f"hook_09: {model} in {path.name} has an UnmarshalJSON this hook does not recognise")
    lines = [
        f"// UnmarshalJSON decodes into a {model} like encoding/json, taking",
        f"// {_UNMARSHAL_MARKER} ({names}) as a clear: the inverse",
        "// of ToMap. As with the default decoder, properties absent from data are left",
        "// untouched; a timestamp or null for a date replaces a clear, as its setters do.",
        signature,
        f"\ttype plain {model}",
        "\tvar raw map[string]json.RawMessage",
        "\tif err := json.Unmarshal(data, &raw); err != nil {",
        "\t\treturn err",
        "\t}",
        "\tif raw == nil { // a JSON null leaves the value untouched",
        "\t\treturn nil",
        "\t}",
        "\t// Only a string literal can be a clear; null decodes to \"\" too but preserves.",
        "\tisEmpty := func(v json.RawMessage) bool {",
        "\t\tvar s string",
        "\t\treturn len(v) > 0 && v[0] == '\"' && json.Unmarshal(v, &s) == nil && s == \"\"",
        "\t}",
    ]
    for prop, _, flag in cleared:
        base = flag[: -len("Cleared")]
        lines += [
            f"\t{base}Value, {base}Present := raw[\"{prop}\"]",
            f"\t{base}Clear := {base}Present && isEmpty({base}Value)",
            f"\tif {base}Clear {{",
            f"\t\tdelete(raw, \"{prop}\")",
            "\t}",
        ]
    lines += [
        "\trest, err := json.Marshal(raw)",
        "\tif err != nil {",
        "\t\treturn err",
        "\t}",
        "\tp := plain(*o)",
        "\tif err := json.Unmarshal(rest, &p); err != nil {",
        "\t\treturn err",
        "\t}",
        f"\t*o = {model}(p)",
    ]
    for _, field, flag in cleared:
        base = flag[: -len("Cleared")]
        lines += [
            f"\tif {base}Clear {{",
            f"\t\to.{field}.Unset()",
            f"\t\to.{flag} = true",
            f"\t}} else if {base}Present {{",
            f"\t\to.{flag} = false",
            "\t}",
        ]
    lines += ["\treturn nil", "}"]
    return text.rstrip("\n") + "\n\n" + "\n".join(lines) + "\n"


def run(ctx) -> None:
    client_dir: Path = ctx["client_dir"]
    spec = json.loads(Path(ctx["spec_path"]).read_text(encoding="utf-8"))
    patched = 0
    for model, props in sorted(_clearable_date_props(spec).items()):
        path = next(
            (f for f in sorted(client_dir.glob("model_*.go"))
             if f"type {model} struct {{" in f.read_text(encoding="utf-8")),
            None,
        )
        if path is None:
            raise SystemExit(f"hook_09: no generated model for schema {model}")
        text = path.read_text(encoding="utf-8")
        cleared = []
        for prop in props:
            text, field, flag = _patch(text, model, prop, path)
            cleared.append((prop, field, flag))
            patched += 1
        text = _unmarshal(text, model, cleared, path)
        path.write_text(text, encoding="utf-8")
    print(f"    made {patched} date-time field(s) clearable")
