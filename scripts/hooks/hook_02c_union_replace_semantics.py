"""Hook 02c — oneOf wrappers replace, rather than merge into, the receiver.

openapi-generator's ``oneOf`` ``UnmarshalJSON`` decodes into the receiver's existing
branch pointers and never clears the others. Decoding one variant and then another
into the *same* value therefore leaves both branches populated::

    var a ProbeAnswerRequest
    json.Unmarshal(`{"outcome":"ERROR","message":"x"}`, &a)
    json.Unmarshal(`{"outcome":"OK","data":{}}`, &a)
    // a.ProbeErrorAnswerRequest != nil && a.ProbeOkAnswerRequest != nil

and ``MarshalJSON`` then sends the stale branch
(orchestration-cluster-api-go#238).

For every generated ``oneOf`` wrapper this hook starts ``UnmarshalJSON`` by zeroing
the receiver, the same replace semantics ``encoding/json`` gives a plain struct
value. Wrappers that hook 02b has already patched (the ``*FilterProperty``
unions) are left as they are.

This hook deliberately changes nothing else: what an unknown or missing
discriminator decodes to is a separate decision (#239).
"""
from __future__ import annotations

import re
from pathlib import Path

_ONEOF_MARKER = "no data in oneOf schemas"
_FUNC_DECL = re.compile(
    r"^func \(dst \*(?P<name>\w+)\) UnmarshalJSON\(data \[\]byte\) error \{\n",
    re.MULTILINE,
)

_RESET = """\
\t// Replace semantics: start from a zero receiver so decoding over a previously
\t// populated value never keeps a branch from the earlier decode, which
\t// MarshalJSON would then send (orchestration-cluster-api-go#238).
\t*dst = %s{}
"""


def run(ctx) -> None:
    client_dir: Path = ctx["client_dir"]
    patched = already = 0
    for path in sorted(client_dir.glob("model_*.go")):
        text = path.read_text(encoding="utf-8")
        if _ONEOF_MARKER not in text:
            continue
        matches = list(_FUNC_DECL.finditer(text))
        if len(matches) != 1:
            raise RuntimeError(
                f"expected exactly one wrapper UnmarshalJSON in {path.name}, found {len(matches)}"
            )
        m = matches[0]
        name = m.group("name")
        if f"\t*dst = {name}{{}}\n" in text:
            already += 1
            continue
        new_text = text[: m.end()] + _RESET % name + text[m.end():]
        path.write_text(new_text, encoding="utf-8")
        patched += 1
    if patched + already == 0:
        raise RuntimeError("found no generated oneOf wrappers; refusing to patch nothing")
    print(f"    reset the receiver in {patched} oneOf wrapper(s) ({already} already did)")
