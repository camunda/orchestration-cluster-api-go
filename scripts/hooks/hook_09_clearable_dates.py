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


def _patch(text: str, model: str, prop: str, path: Path) -> str:
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
    return text


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
        for prop in props:
            text = _patch(text, model, prop, path)
            patched += 1
        path.write_text(text, encoding="utf-8")
    print(f"    made {patched} date-time field(s) clearable")
