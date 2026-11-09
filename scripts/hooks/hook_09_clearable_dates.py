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
A generated model that already has its own ``UnmarshalJSON`` fails the run, as
the two cannot be merged safely.

The affected properties are read from the spec — a schema description bullet
"* `<prop>` - clear by providing an empty String" on a ``date-time`` property —
so a property the spec stops declaring as a date (and that the generator then
types as a string, which can already hold ``""``) is simply no longer patched.
Every generated anchor this hook rewrites must be present: a generator change
that moves one fails the run rather than leaving a half-patched model.
"""
from __future__ import annotations

import json
import re
from pathlib import Path

_CLEAR_BULLET = re.compile(r"\*\s*`(?P<prop>\w+)`\s*-\s*clear by providing an empty String", re.IGNORECASE)


def _clearable_date_props(spec: dict) -> dict[str, list[str]]:
    out: dict[str, list[str]] = {}
    for name, schema in spec.get("components", {}).get("schemas", {}).items():
        props = schema.get("properties") or {}
        for m in _CLEAR_BULLET.finditer(schema.get("description") or ""):
            prop = props.get(m.group("prop")) or {}
            if prop.get("type") == "string" and prop.get("format") == "date-time":
                out.setdefault(name, []).append(m.group("prop"))
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
        raise SystemExit(f"hook_09: {model}.{prop} is not a NullableTime field in {path.name}")
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
