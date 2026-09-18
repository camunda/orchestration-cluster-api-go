"""Hook 01 — Camunda Domain Type System.

openapi-generator does not model Camunda's semantic keys correctly: it collapses
every key/id to a single ``ModelString`` type in struct fields and references a
couple of key types (e.g. ``ElementInstanceKey``, ``AgentInstanceKey``) in API
parameter positions without ever defining them. The result does not compile.

This hook emits ``zz_generated_domain_keys.go`` defining:

  * ``ModelString`` — the base semantic-string type the generator references, plus
    its ``NullableModelString`` helper (mirroring the generator's Nullable pattern).
  * one distinct, validated named string type per semantic key in the spec metadata
    (``type ProcessInstanceKey string`` …), each with ``New*``/``Must*`` constructors
    that enforce the spec's pattern/length constraints, plus ``String`` and
    ``Validate`` methods.
  * a distinct type for every other ``x-semantic-type`` scalar the metadata does not
    classify as a key (e.g. the string ``ScopeKey`` and the integer
    ``LoopIterationId``), so non-key semantic scalars are branded end-to-end.
  * a ``Nullable<Type>`` wrapper for each semantic type that appears in a nullable
    response/model field, mirroring the generator's ``NullableModelString`` pattern.

It then **retypes response/model struct fields** (and their constructors and
accessors) from the generic ``ModelString`` / ``string`` / ``int32`` back to their
specific semantic type — e.g. ``ActivatedJobResult.JobKey`` becomes ``JobKey``
instead of ``ModelString``. Without this, the generator applies the domain-type
system asymmetrically: operation *inputs* (path parameters) keep the specific type
while *response body* fields collapse to the shared base type, erasing the API's
domain typing on the read side.
"""
from __future__ import annotations

import json
import re
from pathlib import Path

_OUT = "zz_generated_domain_keys.go"
_BASE_TYPE = "ModelString"

# Semantic string keys the bundler metadata misses because the upstream schema
# omits the `x-semantic-type` extension. `ResourceKey` is `type: string` (a scalar
# "system-assigned key", a oneOf supertype of ProcessDefinitionKey /
# DecisionRequirementsKey / FormKey / DecisionDefinitionKey — all LongKey), used as
# a path parameter, but without `x-semantic-type` the bundler does not classify it,
# so it is absent from metadata.semanticKeys and openapi-generator emits it as a
# broken oneOf *struct*. We generate it as a validated string newtype here (with
# the LongKey constraints its members share), matching every other key, and delete
# the generator's struct. Each entry names the generator model file to remove and
# whether a bespoke Nullable<Key> wrapper must be emitted (because the deleted file
# defined one that other models reference — e.g. AuditLogResult.resourceKey).
# TODO: remove an entry once upstream adds `x-semantic-type` to that schema (the
# bundler will then classify it and it will arrive via metadata.semanticKeys).
_EXTRA_STRING_KEYS = {
    "ResourceKey": {
        "constraints": {"pattern": "^-?[0-9]+$", "minLength": 1, "maxLength": 25},
        "model_file": "model_resource_key.go",
        "nullable": True,
    },
}

# Non-key semantic scalars the upstream spec carries only as inline `type: string`
# properties — it neither declares a named schema nor tags them `x-semantic-type`,
# so the bundler cannot classify them and there is no ref for the field resolver to
# follow. We still want them branded end-to-end (see issue #57), so we mint a
# newtype here and map the (unambiguous) json property names that carry the scalar
# to it.
# TODO: drop an entry once the *committed* spec has upstream's named
# `x-semantic-type` schema for it. Until then the entry is still load-bearing for
# regeneration from the committed spec. The minted-name guard below makes an entry
# that upstream has already superseded a no-op rather than a duplicate definition,
# so the two can be reconciled in the regeneration PR instead of in lockstep.
_EXTRA_SCALAR_TYPES: dict[str, dict] = {}

_TYPE_DECL = re.compile(r"^type\s+(\w+)\s", re.MULTILINE)
_PKG_DECL = re.compile(r"^package\s+(\w+)", re.MULTILINE)

# Base Go tokens the generator emits for a semantic scalar before this hook rewrites
# it, mapped to the underlying scalar used inside accessor method bodies. `string`
# and `ModelString` cover every semantic string field; `int32`/`int64` cover the
# (rare) integer semantic scalars.
_BASE_TOKENS = {"ModelString", "string", "int32", "int64"}


def _detect_package(client_dir: Path) -> str:
    for name in ("client.go", "configuration.go", "utils.go"):
        p = client_dir / name
        if p.exists():
            m = _PKG_DECL.search(p.read_text(encoding="utf-8"))
            if m:
                return m.group(1)
    return "openapi"


def _existing_types(client_dir: Path) -> set[str]:
    found: set[str] = set()
    for f in client_dir.glob("*.go"):
        if f.name == _OUT:
            continue
        try:
            found.update(_TYPE_DECL.findall(f.read_text(encoding="utf-8")))
        except OSError:
            continue
    return found


def _go_raw_string(pattern: str) -> str:
    # Go raw string literals cannot contain a backtick; fall back to a quoted
    # literal with minimal escaping in that (rare) case.
    if "`" not in pattern:
        return "`" + pattern + "`"
    return '"' + pattern.replace("\\", "\\\\").replace('"', '\\"') + '"'


def _key_block(name: str, constraints: dict, noun: str = "semantic key") -> str:
    pattern = constraints.get("pattern")
    min_len = int(constraints.get("minLength", 0) or 0)
    max_len = int(constraints.get("maxLength", 0) or 0)
    if pattern:
        pat_expr = f"regexp.MustCompile({_go_raw_string(pattern)})"
    else:
        pat_expr = "nil"
    return f"""
// {name} is a Camunda {noun}. Construct it with New{name} (validated) or
// Must{name} (panics on invalid input).
type {name} string

var spec{name} = keySpec{{name: "{name}", pattern: {pat_expr}, min: {min_len}, max: {max_len}}}

// New{name} validates s against the {name} constraints and returns a {name}.
func New{name}(s string) ({name}, error) {{
\tif err := spec{name}.validate(s); err != nil {{
\t\treturn "", err
\t}}
\treturn {name}(s), nil
}}

// Must{name} is like New{name} but panics if s is invalid.
func Must{name}(s string) {name} {{
\tk, err := New{name}(s)
\tif err != nil {{
\t\tpanic(err)
\t}}
\treturn k
}}

// String returns the underlying string value.
func (k {name}) String() string {{ return string(k) }}

// Validate reports whether k satisfies the {name} constraints.
func (k {name}) Validate() error {{ return spec{name}.validate(string(k)) }}
"""


def _int_block(name: str, gotype: str, constraints: dict) -> str:
    minv = constraints.get("minimum")
    maxv = constraints.get("maximum")
    has_min = "true" if minv is not None else "false"
    has_max = "true" if maxv is not None else "false"
    min_lit = int(minv) if minv is not None else 0
    max_lit = int(maxv) if maxv is not None else 0
    conv = "Int32" if gotype == "int32" else "Int64"
    return f"""
// {name} is a Camunda semantic integer identifier. Construct it with New{name}
// (validated) or Must{name} (panics on invalid input).
type {name} {gotype}

var spec{name} = intSpec{{name: "{name}", min: {min_lit}, hasMin: {has_min}, max: {max_lit}, hasMax: {has_max}}}

// New{name} validates v against the {name} constraints and returns a {name}.
func New{name}(v {gotype}) ({name}, error) {{
\tif err := spec{name}.validate(int64(v)); err != nil {{
\t\treturn 0, err
\t}}
\treturn {name}(v), nil
}}

// Must{name} is like New{name} but panics if v is invalid.
func Must{name}(v {gotype}) {name} {{
\tk, err := New{name}(v)
\tif err != nil {{
\t\tpanic(err)
\t}}
\treturn k
}}

// {conv} returns the underlying {gotype} value.
func (k {name}) {conv}() {gotype} {{ return {gotype}(k) }}

// Validate reports whether k satisfies the {name} constraints.
func (k {name}) Validate() error {{ return spec{name}.validate(int64(k)) }}
"""


def _nullable_block(name: str) -> str:
    return f"""
// Nullable{name} is the generator's Nullable wrapper for {name} (referenced by
// generated models such as AuditLogResult).
type Nullable{name} struct {{
\tvalue *{name}
\tisSet bool
}}

func (v Nullable{name}) Get() *{name} {{ return v.value }}
func (v *Nullable{name}) Set(val *{name}) {{ v.value = val; v.isSet = true }}
func (v Nullable{name}) IsSet() bool {{ return v.isSet }}
func (v *Nullable{name}) Unset() {{ v.value = nil; v.isSet = false }}

// NewNullable{name} returns a set Nullable{name} wrapping val.
func NewNullable{name}(val *{name}) *Nullable{name} {{
\treturn &Nullable{name}{{value: val, isSet: true}}
}}

func (v Nullable{name}) MarshalJSON() ([]byte, error) {{ return json.Marshal(v.value) }}
func (v *Nullable{name}) UnmarshalJSON(src []byte) error {{
\tv.isSet = true
\treturn json.Unmarshal(src, &v.value)
}}
"""


def _header(pkg: str) -> str:
    return f"""// Code generated by scripts/hooks/hook_01_domain_type_system.py. DO NOT EDIT.

package {pkg}

import (
\t"encoding/json"
\t"fmt"
\t"regexp"
)

// {_BASE_TYPE} is the base type the OpenAPI generator uses for Camunda semantic
// string keys/ids. The distinct named key types below provide compile-time safety.
type {_BASE_TYPE} string

// NullableModelString is the generator's Nullable wrapper for {_BASE_TYPE}.
type NullableModelString struct {{
\tvalue *{_BASE_TYPE}
\tisSet bool
}}

func (v NullableModelString) Get() *{_BASE_TYPE} {{ return v.value }}
func (v *NullableModelString) Set(val *{_BASE_TYPE}) {{ v.value = val; v.isSet = true }}
func (v NullableModelString) IsSet() bool {{ return v.isSet }}
func (v *NullableModelString) Unset() {{ v.value = nil; v.isSet = false }}

// NewNullableModelString returns a set NullableModelString wrapping val.
func NewNullableModelString(val *{_BASE_TYPE}) *NullableModelString {{
\treturn &NullableModelString{{value: val, isSet: true}}
}}

func (v NullableModelString) MarshalJSON() ([]byte, error) {{ return json.Marshal(v.value) }}
func (v *NullableModelString) UnmarshalJSON(src []byte) error {{
\tv.isSet = true
\treturn json.Unmarshal(src, &v.value)
}}

// keySpec describes the validation constraints for a semantic key.
type keySpec struct {{
\tname    string
\tpattern *regexp.Regexp
\tmin     int
\tmax     int
}}

func (s keySpec) validate(v string) error {{
\tif s.min > 0 && len(v) < s.min {{
\t\treturn fmt.Errorf("%s: value %q is shorter than the minimum length %d", s.name, v, s.min)
\t}}
\tif s.max > 0 && len(v) > s.max {{
\t\treturn fmt.Errorf("%s: value %q is longer than the maximum length %d", s.name, v, s.max)
\t}}
\tif s.pattern != nil && !s.pattern.MatchString(v) {{
\t\treturn fmt.Errorf("%s: value %q does not match pattern %s", s.name, v, s.pattern.String())
\t}}
\treturn nil
}}

// intSpec describes the validation constraints for a semantic integer identifier.
type intSpec struct {{
\tname   string
\tmin    int64
\thasMin bool
\tmax    int64
\thasMax bool
}}

func (s intSpec) validate(v int64) error {{
\tif s.hasMin && v < s.min {{
\t\treturn fmt.Errorf("%s: value %d is less than the minimum %d", s.name, v, s.min)
\t}}
\tif s.hasMax && v > s.max {{
\t\treturn fmt.Errorf("%s: value %d is greater than the maximum %d", s.name, v, s.max)
\t}}
\treturn nil
}}
"""


# --- semantic field retyping -------------------------------------------------

_INT_FORMAT_GOTYPE = {"int32": "int32", "int64": "int64"}


def _load_spec(ctx) -> dict:
    spec_path: Path = ctx["spec_path"]
    return json.loads(spec_path.read_text(encoding="utf-8"))


def _ref_name(node) -> str | None:
    if isinstance(node, dict):
        ref = node.get("$ref")
        if isinstance(ref, str):
            return ref.split("/")[-1]
    return None


_CONSTRAINT_KEYS = ("pattern", "minLength", "maxLength", "minimum", "maximum")


def _resolve_scalar_constraints(sc, schemas, seen=None) -> dict:
    """Resolve the validation constraints for a scalar semantic schema, following
    ``allOf``/``oneOf`` ``$ref`` composition when the schema declares none of its
    own. A ``x-semantic-type`` scalar such as ``ScopeKey`` is modelled as a
    ``oneOf`` of ``ProcessInstanceKey``/``ElementInstanceKey`` (both ``LongKey``)
    and carries no constraints itself, so minting it verbatim would lose the
    ``LongKey`` validation its members share. For ``oneOf`` the member constraints
    are adopted only when every member resolves to the *same* set, so we never
    invent a bound the union does not actually guarantee."""
    if not isinstance(sc, dict):
        return {}
    seen = seen or set()
    direct = {k: sc[k] for k in _CONSTRAINT_KEYS if k in sc}
    if direct:
        return direct
    for frag in sc.get("allOf", []) or []:
        ref = _ref_name(frag)
        if ref and ref in seen:
            continue
        target = schemas.get(ref) if ref else (frag if isinstance(frag, dict) else None)
        if target is not None:
            c = _resolve_scalar_constraints(target, schemas, seen | ({ref} if ref else set()))
            if c:
                return c
    members = sc.get("oneOf")
    if isinstance(members, list) and members:
        resolved = []
        for frag in members:
            ref = _ref_name(frag)
            if ref and ref in seen:
                resolved.append({})
                continue
            target = schemas.get(ref) if ref else (frag if isinstance(frag, dict) else None)
            resolved.append(
                _resolve_scalar_constraints(target, schemas, seen | ({ref} if ref else set()))
                if target is not None
                else {}
            )
        first = resolved[0]
        if first and all(c == first for c in resolved):
            return first
    return {}


def _semantic_schemas(spec: dict) -> dict[str, str]:
    """Map every ``x-semantic-type`` schema name to its Go base token."""
    out: dict[str, str] = {}
    for name, sc in (spec.get("components", {}).get("schemas", {}) or {}).items():
        if isinstance(sc, dict) and "x-semantic-type" in sc:
            if sc.get("type") == "integer":
                out[name] = _INT_FORMAT_GOTYPE.get(sc.get("format", "int32"), "int32")
            else:
                out[name] = "string"
    return out


def _array_item_target(arr_schema, semtypes: dict[str, str]):
    """Return the semantic item type of an ``type: array`` schema, or None.

    An array's ``items`` may reference the semantic scalar directly (``$ref``)
    or via a single-element ``allOf`` wrapper."""
    items = (arr_schema.get("items") or {}) if isinstance(arr_schema, dict) else {}
    it = _ref_name(items)
    if not it and isinstance(items.get("allOf"), list) and len(items["allOf"]) == 1:
        it = _ref_name(items["allOf"][0])
    return it if it in semtypes else None


def _union_map(schemas: dict, semtypes: dict[str, str]) -> dict[frozenset, str]:
    """Map ``frozenset(member schema names) -> union schema name`` for every
    semantic schema that is itself a ``oneOf`` union (e.g. ``ScopeKey`` =
    ``ProcessInstanceKey | ElementInstanceKey``). Lets an inline ``oneOf``
    property resolve to the branded union type regardless of member order."""
    out: dict[frozenset, str] = {}
    for name in semtypes:
        sc = schemas.get(name)
        if isinstance(sc, dict) and isinstance(sc.get("oneOf"), list):
            members = frozenset(r for r in (_ref_name(x) for x in sc["oneOf"]) if r)
            if members:
                out[members] = name
    return out


def _resolve_property(pd, semtypes: dict[str, str], schemas: dict | None = None,
                      union_map: dict | None = None):
    """Resolve a property schema to (target_type, kind) where kind is
    ``scalar`` or ``array``; returns None when it is not a semantic scalar.

    Handles: an inline ``type: array`` with semantic items; a property ``$ref``
    to a *named array* schema (e.g. ``tags`` -> ``TagSet`` -> ``[]Tag``); an
    inline ``oneOf`` whose members match a semantic union schema (e.g.
    ``ElementInstanceKey | ProcessInstanceKey`` -> ``ScopeKey``); and a scalar
    ``$ref`` / single-``allOf`` reference."""
    if not isinstance(pd, dict):
        return None
    schemas = schemas or {}
    union_map = union_map or {}

    # Inline array with semantic items.
    if pd.get("type") == "array":
        it = _array_item_target(pd, semtypes)
        return (it, "array") if it else None

    # Inline ``oneOf`` matching a semantic union schema (e.g. ScopeKey). Checked
    # before the plain-scalar path because such a schema also carries
    # ``type: string``, which would otherwise look like a bare scalar.
    if isinstance(pd.get("oneOf"), list):
        members = frozenset(r for r in (_ref_name(x) for x in pd["oneOf"]) if r)
        u = union_map.get(members)
        if u in semtypes:
            return (u, "scalar")

    # Scalar ``$ref`` / single-``allOf`` reference.
    tgt = _ref_name(pd)
    if not tgt and isinstance(pd.get("allOf"), list) and len(pd["allOf"]) == 1:
        tgt = _ref_name(pd["allOf"][0])
    if tgt in semtypes:
        return (tgt, "scalar")

    # Property ``$ref`` to a *named array* schema (e.g. tags -> TagSet -> []Tag).
    if tgt and isinstance(schemas.get(tgt), dict) and schemas[tgt].get("type") == "array":
        it = _array_item_target(schemas[tgt], semtypes)
        if it:
            return (it, "array")

    return None


def _schema_effective_properties(name, schemas, semtypes, seen, union_map=None):
    """Resolve a named schema's effective property→(target,kind,nullable) map,
    merging ``allOf`` ``$ref`` base fragments recursively (openapi-generator
    flattens ``allOf`` composition into a single Go struct, so an inherited key
    field lands on the derived model just like a locally-declared one)."""
    sc = schemas.get(name)
    if not isinstance(sc, dict):
        return {}
    return _props_from_schema(sc, schemas, semtypes, seen | {name}, union_map)


def _props_from_schema(sc, schemas, semtypes, seen, union_map=None):
    out: dict[str, tuple] = {}
    for frag in sc.get("allOf", []) or []:
        ref = _ref_name(frag)
        if ref and ref not in seen:
            out.update(_schema_effective_properties(ref, schemas, semtypes, seen, union_map))
        elif isinstance(frag, dict):
            out.update(_props_from_schema(frag, schemas, semtypes, seen, union_map))
    for prop, pd in (sc.get("properties") or {}).items():
        r = _resolve_property(pd, semtypes, schemas, union_map)
        if r:
            tgt, kind = r
            nullable = bool(isinstance(pd, dict) and pd.get("nullable"))
            out[prop] = (tgt, kind, nullable)
    return out


def _walk_semantic_properties(node, semtypes, acc, nullable_types, nonsemantic,
                              schemas=None, union_map=None):
    """Recursively collect every semantic property occurrence anywhere in the
    spec (named schemas, ``allOf`` fragments, array ``items``, inline nested
    objects) into ``acc: {prop: set[target]}`` and record nullable targets. Also
    records into ``nonsemantic`` every property name that appears somewhere as a
    plain (non-semantic) string/integer scalar — such names are unsafe for the
    global fallback because the same name brands different, unrelated fields."""
    if isinstance(node, dict):
        props = node.get("properties")
        if isinstance(props, dict):
            for prop, pd in props.items():
                r = _resolve_property(pd, semtypes, schemas, union_map)
                if r:
                    tgt, kind = r
                    acc.setdefault(prop, set()).add(tgt)
                    if kind == "scalar" and isinstance(pd, dict) and pd.get("nullable"):
                        nullable_types.add(tgt)
                elif _is_plain_scalar(pd):
                    nonsemantic.add(prop)
        for v in node.values():
            _walk_semantic_properties(v, semtypes, acc, nullable_types, nonsemantic,
                                      schemas, union_map)
    elif isinstance(node, list):
        for v in node:
            _walk_semantic_properties(v, semtypes, acc, nullable_types, nonsemantic,
                                      schemas, union_map)


def _is_plain_scalar(pd) -> bool:
    """A string/integer property that is NOT a semantic scalar (so it maps to a
    bare Go ``string``/``int32``/``int64`` the global fallback must not touch)."""
    if not isinstance(pd, dict):
        return False
    t = pd.get("type")
    if t in ("string", "integer"):
        return True
    if t == "array":
        items = pd.get("items")
        return isinstance(items, dict) and items.get("type") in ("string", "integer")
    return False


def _semantic_fields(spec: dict, semtypes: dict[str, str]):
    """Build the per-named-schema map ``{schema: {json_prop: target}}`` (with
    ``allOf`` bases merged), a global unambiguous ``{json_prop: target}`` fallback
    (for inline schemas the generator names itself), and the set of types used in
    a nullable field (which need a ``Nullable<Type>`` wrapper)."""
    schemas = spec.get("components", {}).get("schemas", {}) or {}
    union_map = _union_map(schemas, semtypes)

    by_schema: dict[str, dict[str, str]] = {}
    nullable_types: set[str] = set()
    for sname, sc in schemas.items():
        if not isinstance(sc, dict):
            continue
        eff = _schema_effective_properties(sname, schemas, semtypes, set(), union_map)
        for prop, (tgt, kind, nullable) in eff.items():
            by_schema.setdefault(sname, {})[prop] = tgt
            if kind == "scalar" and nullable:
                nullable_types.add(tgt)

    # Global fallback: a property name is safe to brand on any (possibly
    # inline-named) model only when it resolves to exactly one semantic type
    # everywhere it appears AND never appears as a plain non-semantic scalar
    # (otherwise a generic name like ``id``/``name`` would mis-brand unrelated
    # fields).
    occurrences: dict[str, set] = {}
    nonsemantic: set[str] = set()
    _walk_semantic_properties(spec, semtypes, occurrences, nullable_types, nonsemantic,
                              schemas, union_map)
    global_prop: dict[str, str] = {
        prop: next(iter(types))
        for prop, types in occurrences.items()
        if len(types) == 1 and prop not in nonsemantic
    }

    return by_schema, global_prop, nullable_types


def _base_token(gotype: str) -> str | None:
    t = gotype
    if t.startswith("[]"):
        t = t[2:]
    if t.startswith("*"):
        t = t[1:]
    if t.startswith("Nullable"):
        rest = t[len("Nullable"):]
        if rest == "ModelString":
            return "ModelString"
        if rest in ("Int32", "Int64"):
            return rest.lower()
        return "string"
    return t if t in _BASE_TOKENS else None


def _new_field_type(gotype: str, target: str) -> str:
    prefix = ""
    t = gotype
    if t.startswith("[]"):
        prefix, t = "[]", t[2:]
    elif t.startswith("*"):
        prefix, t = "*", t[1:]
    if t.startswith("Nullable"):
        return prefix + "Nullable" + target
    return prefix + target


_STRUCT_FIELD = re.compile(
    r'^\t(?P<name>\w+)\s+(?P<type>[\w.*\[\]]+)\s+`json:"(?P<json>[^,"]+)',
    re.MULTILINE,
)


def _rewrite_model_file(text: str, schema: str, field_targets: dict[str, str]) -> tuple[str, int]:
    struct_re = re.compile(r"(?ms)^type %s struct \{\n(?P<body>.*?)\n\}" % re.escape(schema))
    m = struct_re.search(text)
    if not m:
        return text, 0
    body = m.group("body")

    changed = 0
    for fm in _STRUCT_FIELD.finditer(body):
        go_name = fm.group("name")
        go_type = fm.group("type")
        json_key = fm.group("json")
        target = field_targets.get(json_key)
        if not target:
            continue
        base = _base_token(go_type)
        if base is None or base == target:
            continue  # already branded or not a recognised base
        new_type = _new_field_type(go_type, target)
        changed += 1

        # 1. struct field declaration
        text = re.sub(
            r"(?m)^(\t+%s\s+)%s(\s)" % (re.escape(go_name), re.escape(go_type)),
            lambda mm: mm.group(1) + new_type + mm.group(2),
            text,
            count=1,
        )

        # 2. constructor parameter (required fields only appear here)
        param = go_name[0].lower() + go_name[1:]
        text = re.sub(
            r"(\b%s )%s([,)])" % (re.escape(param), re.escape(go_type)),
            lambda mm: mm.group(1) + new_type + mm.group(2),
            text,
        )

        # 3. accessor method bodies (Get / GetOk / Set) use the underlying base
        #    token — replace it there with the branded type. The generated doc
        #    comments additionally name the field's *wrapper* type (e.g. "given
        #    NullableString"), a single capitalized token the base-token pass
        #    leaves stale for Nullable fields (whose base is the lowercase
        #    primitive); rewrite that wrapper token too so the docs match the
        #    regenerated signatures.
        base_word = re.compile(r"\b%s\b" % re.escape(base))
        old_wrapper = go_type.lstrip("*[]")
        new_wrapper = new_type.lstrip("*[]")
        wrapper_word = (
            re.compile(r"\b%s\b" % re.escape(old_wrapper))
            if old_wrapper != new_wrapper
            else None
        )

        def _rewrite_block(bm):
            s = base_word.sub(target, bm.group(0))
            if wrapper_word is not None:
                s = wrapper_word.sub(new_wrapper, s)
            return s

        for meth in (f"Get{go_name}", f"Get{go_name}Ok", f"Set{go_name}"):
            block_re = re.compile(
                r"(?ms)^(?://[^\n]*\n)*func \(o \*?%s\) %s\(.*?\n\}\n"
                % (re.escape(schema), re.escape(meth))
            )
            text = block_re.sub(_rewrite_block, text, count=1)

    return text, changed


def _scan_nullable_usage(client_dir: Path, by_schema, global_prop) -> set[str]:
    """Scan the generated model files for struct fields whose json prop maps to a
    semantic target AND whose generated Go type carries a ``Nullable`` prefix.
    The openapi-generator decides on a per-field basis whether an optional field
    becomes a ``Nullable`` wrapper (e.g. optional integer refs land as
    ``NullableInt32``), which the spec ``nullable`` flag alone does not predict —
    so the authoritative signal for which ``Nullable<Type>`` wrappers we must mint
    is the generated token itself."""
    needed: set[str] = set()
    struct_hdr = re.compile(r"(?m)^type (\w+) struct \{")
    field_struct = re.compile(r"(?ms)^type (?P<name>\w+) struct \{\n(?P<body>.*?)\n\}")
    for f in sorted(client_dir.glob("model_*.go")):
        try:
            text = f.read_text(encoding="utf-8")
        except OSError:
            continue
        for sm in field_struct.finditer(text):
            struct = sm.group("name")
            targets = dict(global_prop)
            targets.update(by_schema.get(struct, {}))
            if not targets:
                continue
            for fm in _STRUCT_FIELD.finditer(sm.group("body")):
                target = targets.get(fm.group("json"))
                if not target:
                    continue
                go_type = fm.group("type")
                bare = go_type.lstrip("[]*")
                if bare.startswith("Nullable"):
                    needed.add(target)
    return needed


def _rewrite_models(client_dir: Path, by_schema, global_prop) -> tuple[int, int]:
    files_changed = 0
    fields_changed = 0
    struct_hdr = re.compile(r"(?m)^type (\w+) struct \{")
    for f in sorted(client_dir.glob("model_*.go")):
        try:
            text = f.read_text(encoding="utf-8")
        except OSError:
            continue
        original = text
        touched = 0
        # A model file may declare more than one struct (the top-level model plus
        # inline nested schemas the generator names itself). Rewrite each: prefer
        # the named-schema map, falling back to the global unambiguous map for
        # inline structs that have no matching component schema.
        for struct in struct_hdr.findall(text):
            targets = dict(global_prop)
            targets.update(by_schema.get(struct, {}))
            if not targets:
                continue
            text, n = _rewrite_model_file(text, struct, targets)
            touched += n
        if text != original:
            f.write_text(text, encoding="utf-8")
            files_changed += 1
            fields_changed += touched
    return files_changed, fields_changed


def run(ctx) -> None:
    client_dir: Path = ctx["client_dir"]
    keys = ctx["metadata"].get("semanticKeys", [])
    pkg = _detect_package(client_dir)

    spec = _load_spec(ctx)
    semtypes = _semantic_schemas(spec)
    by_schema, global_prop, nullable_types = _semantic_fields(spec, semtypes)

    # Brand the hand-maintained non-key scalars the spec leaves as inline strings
    # (see _EXTRA_SCALAR_TYPES). Their json property names are unambiguous, so a
    # direct global mapping retypes every field that carries them. This override
    # deliberately wins over the plain-string exclusion in the resolver (these
    # names appear as bare strings precisely because upstream never tagged them).
    for tname, cfg in _EXTRA_SCALAR_TYPES.items():
        for prop in cfg.get("props", ()):
            global_prop[prop] = tname

    # Remove the generator's mis-modelled structs for the extra string keys BEFORE
    # scanning existing types, so those names are free for our newtype definitions.
    removed_models = []
    for name, cfg in _EXTRA_STRING_KEYS.items():
        model_file = client_dir / cfg["model_file"]
        if model_file.exists():
            model_file.unlink()
            removed_models.append(cfg["model_file"])

    # x-semantic-type schemas the generator mis-models as an (empty) struct because
    # they use oneOf/anyOf composition (e.g. ScopeKey, a oneOf supertype of
    # ProcessInstanceKey / ElementInstanceKey). Delete the struct model so the name
    # is free for the scalar newtype minted below — exactly as for the oneOf
    # ResourceKey handled via _EXTRA_STRING_KEYS. Struct fields already reference
    # these schemas by name, so minting them as the proper scalar retypes those
    # fields for free.
    schemas = spec.get("components", {}).get("schemas", {}) or {}
    for name, sc in schemas.items():
        if not (isinstance(sc, dict) and "x-semantic-type" in sc):
            continue
        if not any(k in sc for k in ("oneOf", "anyOf")):
            continue
        for mf in client_dir.glob("model_*.go"):
            if mf.name in removed_models:
                continue
            try:
                txt = mf.read_text(encoding="utf-8")
            except OSError:
                continue
            if re.search(r"(?m)^type %s struct \{" % re.escape(name), txt):
                mf.unlink()
                removed_models.append(mf.name)
                break

    existing = _existing_types(client_dir)

    # Track which semantic type names we mint here so we can emit the matching
    # Nullable<Type> wrappers and know what the metadata scan already covers.
    minted: set[str] = set()

    parts = [_header(pkg)]
    generated = 0
    skipped = []
    # Hand-maintained _EXTRA_* entries upstream has since superseded with a real
    # x-semantic-type schema, so they now arrive via the spec-driven path too.
    redundant: list[str] = []
    # Names whose Nullable<Type> wrapper an _EXTRA_* entry emitted itself. Derived
    # from what was actually written, not from the entry declaring `nullable`: a
    # superseded entry emits nothing, and must not suppress the wrapper the
    # spec-driven scan below would otherwise generate.
    emitted_nullable: set[str] = set()
    # Sort by name so the generated output is stable regardless of the order in
    # which semanticKeys appear in the (re-bundled) spec metadata.
    for key in sorted(keys, key=lambda k: (k.get("name") or "")):
        name = key.get("name")
        if not name or name == _BASE_TYPE:
            continue
        if name in existing:
            skipped.append(name)
            continue
        parts.append(_key_block(name, key.get("constraints", {}) or {}))
        minted.add(name)
        generated += 1

    # Extra string keys the metadata missed (see _EXTRA_STRING_KEYS).
    for name in sorted(_EXTRA_STRING_KEYS):
        if name in existing:
            skipped.append(name)
            continue
        if name in minted:
            redundant.append(name)
            continue
        cfg = _EXTRA_STRING_KEYS[name]
        parts.append(_key_block(name, cfg.get("constraints", {}) or {}))
        minted.add(name)
        if cfg.get("nullable"):
            parts.append(_nullable_block(name))
            emitted_nullable.add(name)
        generated += 1

    # Hand-maintained non-key scalars the spec carries only as inline strings
    # (see _EXTRA_SCALAR_TYPES). Minted as validated newtypes
    # so the fields branded above (via the global-prop override) resolve.
    for name in sorted(_EXTRA_SCALAR_TYPES):
        if name in existing:
            skipped.append(name)
            continue
        if name in minted:
            redundant.append(name)
            continue
        cfg = _EXTRA_SCALAR_TYPES[name]
        if cfg.get("base", "string") == "string":
            parts.append(
                _key_block(
                    name,
                    cfg.get("constraints", {}) or {},
                    noun=cfg.get("noun", "semantic key"),
                )
            )
        else:
            parts.append(_int_block(name, cfg["base"], cfg.get("constraints", {}) or {}))
        minted.add(name)
        if cfg.get("nullable"):
            parts.append(_nullable_block(name))
            emitted_nullable.add(name)
        generated += 1

    # Non-key `x-semantic-type` scalars the bundler metadata does not classify as
    # keys (e.g. the string ``ScopeKey`` and the integer ``LoopIterationId``). The
    # metadata only tracks key kinds, so these would otherwise have no distinct
    # type anywhere and consumers would get a bare ``string``/``int32``.
    for name in sorted(semtypes):
        if name in existing or name in minted:
            continue
        sc = spec["components"]["schemas"].get(name, {})
        constraints = _resolve_scalar_constraints(sc, spec["components"]["schemas"])
        gotype = semtypes[name]
        if gotype == "string":
            parts.append(_key_block(name, constraints))
        else:
            parts.append(_int_block(name, gotype, constraints))
        minted.add(name)
        generated += 1

    # Nullable<Type> wrappers for every semantic type that appears in a nullable
    # response/model field, mirroring NullableModelString. ModelString and the
    # extra keys already have their wrappers emitted above.
    already_nullable = {_BASE_TYPE} | emitted_nullable
    nullable_types |= _scan_nullable_usage(client_dir, by_schema, global_prop)
    nullable_generated = 0
    for name in sorted(nullable_types):
        if name in already_nullable:
            continue
        if name not in minted and name not in existing:
            continue
        parts.append(_nullable_block(name))
        nullable_generated += 1

    out_path = client_dir / _OUT
    out_path.write_text("\n".join(parts), encoding="utf-8")
    print(f"    generated {generated} semantic key types + ModelString into {_OUT}")
    if nullable_generated:
        print(f"    generated {nullable_generated} Nullable<Type> wrapper(s) for nullable semantic fields")
    if removed_models:
        print(f"    removed {len(removed_models)} mis-modelled key struct(s): {', '.join(sorted(removed_models))}")
    if skipped:
        print(f"    skipped {len(skipped)} keys already declared by the generator: {', '.join(sorted(skipped))}")
    if redundant:
        print(
            f"    WARNING: {len(redundant)} hand-maintained entr(y/ies) now supersed"
            f"ed by upstream x-semantic-type schemas: {', '.join(sorted(redundant))}"
            " — delete them from _EXTRA_STRING_KEYS/_EXTRA_SCALAR_TYPES"
        )

    # Retype the response/model struct fields (and their constructors/accessors)
    # from the generic base tokens back to their specific semantic type.
    files_changed, fields_changed = _rewrite_models(client_dir, by_schema, global_prop)
    print(f"    retyped {fields_changed} semantic field(s) across {files_changed} model file(s)")
