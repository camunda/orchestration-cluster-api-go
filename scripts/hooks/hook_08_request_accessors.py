"""Hook 08 — request-builder field accessors.

The openapi-generator Go shape keeps optional request parameters in unexported
fields of the ``Api<Op>Request`` struct, surfaced only through fluent setters
(``r.TenantId(t)``). The ergonomic facade sometimes needs to *read* an optional
parameter back — e.g. to inject the configured default tenant only when the
caller did not set one (issue #79) — so this hook emits an accessor for each
optional (pointer-typed) field of every request struct:

    // GetTenantId returns the tenantId field value, or nil when unset.
    func (r ApiCreateDeploymentRequest) GetTenantId() *string { return r.tenantId }

The accessor name is derived from the field name; fields that already have a
same-named method are skipped.
"""
from __future__ import annotations

import re
from pathlib import Path

# Request struct and its fields, e.g.
#   type ApiCreateDeploymentRequest struct {
#       ...
#       tenantId *string
#   }
_REQUEST_STRUCT = re.compile(
    r"type (?P<req>Api\w+Request) struct \{(?P<body>.*?)\n\}", re.DOTALL
)
# An optional (pointer-typed) request field, e.g. "\ttenantId *string".
_PTR_FIELD = re.compile(r"^\t(?P<name>[a-z]\w*)\s+\*(?P<typ>[\w.\[\]]+)\s*$", re.MULTILINE)


def run(ctx) -> None:
    client_dir: Path = ctx["client_dir"]
    accessors = 0
    for f in sorted(client_dir.glob("api_*.go")):
        text = f.read_text(encoding="utf-8")
        blocks = []
        for struct in _REQUEST_STRUCT.finditer(text):
            req = struct.group("req")
            for field in _PTR_FIELD.finditer(struct.group("body")):
                name, typ = field.group("name"), field.group("typ")
                accessor = "Get" + name[0].upper() + name[1:]
                if f"func (r {req}) {accessor}(" in text:
                    continue
                blocks.append(
                    f"// {accessor} returns the {name} field value, or nil when unset.\n"
                    f"func (r {req}) {accessor}() *{typ} {{ return r.{name} }}\n"
                )
                accessors += 1
        if blocks:
            text = text.rstrip("\n") + "\n\n" + "\n".join(blocks)
            f.write_text(text, encoding="utf-8")
    print(f"    emitted {accessors} request-field accessor(s)")
