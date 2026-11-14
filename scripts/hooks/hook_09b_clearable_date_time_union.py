"""Hook 09b — strict ClearableDateTime union codec.

The spec models ``ClearableDateTime`` as ``anyOf`` a ``date-time`` string and an
empty string (``maxLength: 0``). openapi-generator's anyOf codec gets this wrong:

* it tries the ``String`` branch first and accepts *any* string, so an invalid
  timestamp such as ``"not-a-date"`` decodes successfully and is re-sent verbatim;
* it never resets the branches, so decoding ``null`` (or a string) over a
  populated value keeps the stale timestamp;
* ``MarshalJSON`` of an unset union returns ``nil, nil``, which encoding/json
  reports as ``unexpected end of JSON input``.

This hook replaces both methods: ``null`` resets to the unset (preserve) value,
``""`` selects the clear branch, any other string must be an RFC 3339 timestamp,
and anything else is rejected. Marshaling emits ``null`` when unset and refuses a
non-empty ``String`` branch, which the schema cannot carry.
"""
from __future__ import annotations

import re
from pathlib import Path

_UNMARSHAL = '''func (dst *ClearableDateTime) UnmarshalJSON(data []byte) error {
	// Reset both branches so a decode never leaves a stale value behind.
	dst.String = nil
	dst.TimeTime = nil
	if string(bytes.TrimSpace(data)) == "null" {
		return nil // null preserves the persisted value
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("ClearableDateTime: expected a string or null: %w", err)
	}
	if s == "" {
		dst.String = &s
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return fmt.Errorf("ClearableDateTime: %q is neither an empty string nor an RFC 3339 date-time: %w", s, err)
	}
	dst.TimeTime = &t
	return nil
}
'''

_MARSHAL = '''func (src ClearableDateTime) MarshalJSON() ([]byte, error) {
	if src.String != nil {
		if *src.String != "" {
			return nil, fmt.Errorf("ClearableDateTime: String branch must be empty (clear), got %q", *src.String)
		}
		return json.Marshal(*src.String)
	}
	if src.TimeTime != nil {
		return json.Marshal(src.TimeTime)
	}
	return []byte("null"), nil // unset: preserve the persisted value
}
'''


def _replace_func(text: str, sig: str, body: str, path: Path) -> str:
    m = re.search(r"^" + re.escape(sig) + r".*?^\}\n", text, re.MULTILINE | re.DOTALL)
    if m is None:
        raise SystemExit(f"hook_09b: {sig!r} not found in {path.name}")
    return text[: m.start()] + body + text[m.end():]


def run(ctx) -> None:
    path: Path = ctx["client_dir"] / "model_clearable_date_time.go"
    if not path.exists():
        print("    model_clearable_date_time.go absent; nothing to patch")
        return
    text = path.read_text(encoding="utf-8")
    text = _replace_func(text, "func (dst *ClearableDateTime) UnmarshalJSON(", _UNMARSHAL, path)
    text = _replace_func(text, "func (src ClearableDateTime) MarshalJSON(", _MARSHAL, path)
    if '"bytes"' not in text:
        text = text.replace('import (\n', 'import (\n\t"bytes"\n', 1)
    path.write_text(text, encoding="utf-8")
    print("    patched ClearableDateTime codec")
