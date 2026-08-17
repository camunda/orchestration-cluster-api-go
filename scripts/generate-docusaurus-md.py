#!/usr/bin/env python3
"""
Convert Go doc JSON metadata into Docusaurus-compatible markdown pages for the
Go SDK documentation published at https://docs.camunda.io.

Usage:
    python3 scripts/generate-docusaurus-md.py [--validate-links] [--readme-only]

Input:
    README.md                       - guide content (split by H2)
    docs-json/camunda.json          - exported surface of the root SDK package
    docs-json/domain-keys.json      - exported surface of the generated key types
    examples/operation-map.json     - operationId -> example region map
    examples/*.go                   - compilable examples with region tags

Output:
    docs-md/go-sdk.md               - landing page (sibling of section directory)
    docs-md/go-sdk/*.md             - per-section guide pages (from README H2s)
    docs-md/go-sdk/api-reference/   - API reference pages + _category_.json

The doc JSON files are produced by `make docs-json`, which runs `cmd/docgen`.
Unlike rustdoc JSON in the Rust SDK, that schema is ours, so it is stable by
construction and needs no format-version gate.
"""

from __future__ import annotations

import json
import re
import sys
import textwrap
from dataclasses import dataclass, field
from pathlib import Path

# ---------------------------------------------------------------------------
# Paths
# ---------------------------------------------------------------------------

REPO_ROOT = Path(__file__).resolve().parent.parent
README_PATH = REPO_ROOT / "README.md"
EXAMPLES_DIR = REPO_ROOT / "examples"
OPERATION_MAP_PATH = EXAMPLES_DIR / "operation-map.json"

DOCS_JSON_DIR = REPO_ROOT / "docs-json"
SDK_JSON_PATH = DOCS_JSON_DIR / "camunda.json"
KEYS_JSON_PATH = DOCS_JSON_DIR / "domain-keys.json"

DOCS_MD_DIR = REPO_ROOT / "docs-md"
SECTION_DIR = DOCS_MD_DIR / "go-sdk"
OUTPUT_DIR = SECTION_DIR / "api-reference"

GITHUB_BLOB = "https://github.com/camunda/orchestration-cluster-api-go/blob/main"
PKG_GO_DEV = "https://pkg.go.dev/github.com/camunda/orchestration-cluster-api-go"
PKG_GO_DEV_CLIENT = f"{PKG_GO_DEV}/client"

# ---------------------------------------------------------------------------
# Frontmatter helpers
# ---------------------------------------------------------------------------

FRONTMATTER_TEMPLATE = textwrap.dedent("""\
    ---
    title: "{title}"
    sidebar_label: "{label}"
    mdx:
      format: md
    ---
""")

LANDING_FRONTMATTER = textwrap.dedent("""\
    ---
    id: go-sdk
    title: "Go SDK (Technical Preview)"
    sidebar_label: "Go SDK (Technical Preview)"
    sidebar_position: 1
    mdx:
      format: md
    ---

""")


def frontmatter(title: str, label: str | None = None) -> str:
    return FRONTMATTER_TEMPLATE.format(
        title=_escape_yaml(title),
        label=_escape_yaml(label or title),
    )


def _escape_yaml(s: str) -> str:
    return s.replace('"', '\\"')


def _make_section_frontmatter(doc_id: str, title: str, sidebar_position: int) -> str:
    return (
        f"---\n"
        f"id: {doc_id}\n"
        f'title: "{_escape_yaml(title)}"\n'
        f'sidebar_label: "{_escape_yaml(title)}"\n'
        f"sidebar_position: {sidebar_position}\n"
        f"mdx:\n"
        f"  format: md\n"
        f"---\n\n"
    )


# ---------------------------------------------------------------------------
# Technical Preview banner (injected after the first H1 on every page)
# ---------------------------------------------------------------------------

TECH_PREVIEW_BANNER = (
    "\n:::caution Technical Preview\n"
    "The Go SDK is a **technical preview**. Its API surface may still evolve and "
    "changes may not follow semantic versioning. Pin an exact version if you need "
    "stability.\n"
    ":::\n"
)


def inject_tech_preview_banner(content: str) -> str:
    """Insert the Technical Preview banner after the first H1 heading."""
    m = re.search(r"^#\s+.+$", content, re.MULTILINE)
    if m:
        pos = m.end()
        return content[:pos] + "\n" + TECH_PREVIEW_BANNER + content[pos:]
    return content


# ---------------------------------------------------------------------------
# Link rewriting
# ---------------------------------------------------------------------------

# Depth for the landing page: apis-tools/go-sdk.md
_LANDING_PAGE_DEPTH = 1
# Depth for section pages: apis-tools/go-sdk/<slug>.md
_SECTION_PAGE_DEPTH = 2
# Depth for api-reference pages: apis-tools/go-sdk/api-reference/<slug>.md
_API_REFERENCE_DEPTH = 3
# sidebar_position for the API Reference category (always last)
_API_REFERENCE_POSITION = 100

_URL_PATH_OVERRIDES: dict[str, str] = {
    "camunda-api-rest": "orchestration-cluster-api-rest",
}

_DOCS_LINK_RE = re.compile(r"\[([^\]]*)\]\(https?://docs\.camunda\.io/docs/(?:next/)?(.*?)\)")


def _rewrite_docs_links(content: str, depth: int) -> str:
    """Rewrite absolute docs.camunda.io links to site-relative links."""
    prefix = "../" * depth

    def _replace(m: re.Match) -> str:  # type: ignore[type-arg]
        text = m.group(1)
        url_path = m.group(2).rstrip("/")
        for old, new in _URL_PATH_OVERRIDES.items():
            url_path = url_path.replace(old, new)
        return f"[{text}]({prefix}{url_path}.md)"

    return _DOCS_LINK_RE.sub(_replace, content)


# Repo-relative markdown links (e.g. `[LICENSE](./LICENSE)`) are meaningless once
# the page is copied into camunda-docs. Point them at GitHub instead.
_REPO_LINK_RE = re.compile(r"\[([^\]]+)\]\((?!https?://|#|mailto:|\.\./)([^)\s]+)\)")


def rewrite_repo_links(content: str) -> str:
    def _replace(m: re.Match) -> str:  # type: ignore[type-arg]
        text, target = m.group(1), m.group(2)
        if target.endswith(".md") and "/" not in target:
            # Sibling generated page - leave alone.
            return m.group(0)
        return f"[{text}]({GITHUB_BLOB}/{target.lstrip('./')})"

    return _REPO_LINK_RE.sub(_replace, content)


# ---------------------------------------------------------------------------
# Doc JSON model
# ---------------------------------------------------------------------------


class DocJSONError(RuntimeError):
    pass


@dataclass
class Func:
    name: str
    signature: str
    docs: str
    recv: str = ""


@dataclass
class Field:
    name: str
    type_str: str
    docs: str


@dataclass
class Value:
    names: list[str]
    docs: str
    decl: str


@dataclass
class TypeItem:
    name: str
    kind: str  # struct | interface | func | alias | basic
    docs: str
    decl: str = ""
    fields: list[Field] = field(default_factory=list)
    consts: list[Value] = field(default_factory=list)
    vars: list[Value] = field(default_factory=list)
    funcs: list[Func] = field(default_factory=list)
    methods: list[Func] = field(default_factory=list)

    @property
    def summary(self) -> str:
        return _first_line(self.docs)


@dataclass
class Package:
    name: str
    import_path: str
    docs: str
    consts: list[Value]
    vars: list[Value]
    funcs: list[Func]
    types: dict[str, TypeItem]


def _load_funcs(raw: list[dict]) -> list[Func]:
    return [
        Func(
            name=f["name"],
            signature=f.get("signature", ""),
            docs=(f.get("doc") or "").strip(),
            recv=f.get("recv", ""),
        )
        for f in raw or []
    ]


def _load_values(raw: list[dict]) -> list[Value]:
    return [
        Value(
            names=v.get("names") or [],
            docs=(v.get("doc") or "").strip(),
            decl=v.get("decl") or "",
        )
        for v in raw or []
    ]


def load_doc_json(path: Path) -> Package:
    if not path.is_file():
        raise FileNotFoundError(
            f"Go doc JSON not found: {path}\nRun `make docs-json` first."
        )
    data = json.loads(path.read_text(encoding="utf-8"))
    for key in ("name", "importPath", "types"):
        if key not in data:
            raise DocJSONError(
                f"{path.name}: missing required key {key!r}. "
                "Regenerate with `make docs-json` — the cmd/docgen schema has changed."
            )
    types: dict[str, TypeItem] = {}
    for t in data["types"]:
        types[t["name"]] = TypeItem(
            name=t["name"],
            kind=t.get("kind", "basic"),
            docs=(t.get("doc") or "").strip(),
            decl=t.get("decl") or "",
            fields=[
                Field(name=f["name"], type_str=f.get("type", ""), docs=(f.get("doc") or "").strip())
                for f in t.get("fields") or []
            ],
            consts=_load_values(t.get("consts")),
            vars=_load_values(t.get("vars")),
            funcs=_load_funcs(t.get("funcs")),
            methods=_load_funcs(t.get("methods")),
        )
    return Package(
        name=data["name"],
        import_path=data["importPath"],
        docs=(data.get("doc") or "").strip(),
        consts=_load_values(data.get("consts")),
        vars=_load_values(data.get("vars")),
        funcs=_load_funcs(data.get("funcs")),
        types=types,
    )


def _first_line(docs: str) -> str:
    if not docs:
        return ""
    for para in docs.split("\n\n"):
        text = " ".join(line.strip() for line in para.strip().splitlines() if line.strip())
        if text and not text.startswith("```"):
            return text
    return ""


# ---------------------------------------------------------------------------
# Go doc comment -> Markdown
# ---------------------------------------------------------------------------

# Go doc links: [Name], [pkg.Name], [*pkg.Name]. Docusaurus has no symbol graph,
# so reduce them to inline code rather than leaving literal brackets on the page.
_GO_DOC_LINK_RE = re.compile(r"\[(\*?(?:\w+\.)?[A-Z]\w*(?:\.\w+)?)\](?![(\[:])")
# `[Text]: https://...` link definitions are valid Markdown and are left alone;
# `[Text]: pkg.Symbol` definitions are not and are dropped.
_GO_DOC_LINKDEF_RE = re.compile(r"^\[[^\]]+\]:\s*(?!https?://|/)\S+\s*$", re.MULTILINE)

_MD_HEADING_RE = re.compile(r"^(#{1,6}) (.+)$")
_INDENT_RE = re.compile(r"^(?:\t| {4,})")


def _go_doc_code_blocks(docs: str) -> str:
    """Convert Go doc indented code blocks into fenced ```go blocks.

    Go doc comments mark code by indentation. An indented block survives into
    Markdown as a code block either way, but fencing it lets Docusaurus apply Go
    syntax highlighting.
    """
    lines = docs.split("\n")
    out: list[str] = []
    i = 0
    while i < len(lines):
        if not _INDENT_RE.match(lines[i]):
            out.append(lines[i])
            i += 1
            continue
        block: list[str] = []
        while i < len(lines) and (_INDENT_RE.match(lines[i]) or not lines[i].strip()):
            block.append(lines[i])
            i += 1
        # Trailing blank lines belong to the surrounding prose, not the block.
        while block and not block[-1].strip():
            block.pop()
            i -= 1
        if not block:
            continue
        out.append("```go")
        out.extend(textwrap.dedent("\n".join(block)).split("\n"))
        out.append("```")
    return "\n".join(out)


def _normalize_docs(docs: str, depth: int, heading_base: int = 0) -> str:
    """Prepare a Go doc comment for Docusaurus markdown.

    Go doc headings (`# Heading`) are shifted down by ``heading_base`` so they
    nest under the section that hosts the prose rather than colliding with it.
    """
    if not docs:
        return ""
    docs = _GO_DOC_LINKDEF_RE.sub("", docs)
    docs = _GO_DOC_LINK_RE.sub(lambda m: f"`{m.group(1)}`", docs)
    docs = _go_doc_code_blocks(docs)
    docs = _rewrite_docs_links(docs, depth)

    out: list[str] = []
    in_fence = False
    for line in docs.split("\n"):
        if line.startswith("```"):
            in_fence = not in_fence
            out.append(line)
            continue
        if in_fence:
            out.append(line)
            continue
        heading = _MD_HEADING_RE.match(line)
        if heading and heading_base:
            level = min(len(heading.group(1)) + heading_base, 6)
            line = f"{'#' * level} {heading.group(2)}"
        out.append(line)
    return "\n".join(out).strip()


# ---------------------------------------------------------------------------
# Example inlining (operation-map.json + `// region` tags)
# ---------------------------------------------------------------------------

_REGION_RE_TEMPLATE = r"^[ \t]*//\s*region\s+{name}\s*$(.*?)^[ \t]*//\s*endregion\s+{name}\s*$"


def load_examples() -> dict[str, str]:
    """Map facade method name -> rendered example body.

    The `region` field of operation-map.json is the facade method name, so the
    map keys straight onto `CamundaClient` methods with no case conversion.
    """
    if not OPERATION_MAP_PATH.is_file():
        print(f"  (no {OPERATION_MAP_PATH.name}; skipping example inlining)")
        return {}
    op_map = json.loads(OPERATION_MAP_PATH.read_text(encoding="utf-8"))
    out: dict[str, str] = {}
    cache: dict[Path, str] = {}
    for entries in op_map.values():
        if not entries:
            continue
        entry = entries[0]
        src = EXAMPLES_DIR / entry["file"]
        if src not in cache:
            if not src.is_file():
                print(f"  WARNING: operation-map references missing file {entry['file']}")
                cache[src] = ""
            else:
                cache[src] = src.read_text(encoding="utf-8")
        body = cache[src]
        if not body:
            continue
        pattern = _REGION_RE_TEMPLATE.format(name=re.escape(entry["region"]))
        m = re.search(pattern, body, re.MULTILINE | re.DOTALL)
        if not m:
            print(f"  WARNING: region '{entry['region']}' not found in {entry['file']}")
            continue
        out[entry["region"]] = textwrap.dedent(m.group(1)).strip("\n")
    return out


# ---------------------------------------------------------------------------
# Markdown helpers
# ---------------------------------------------------------------------------


def _md_escape_cell(text: str) -> str:
    return text.replace("|", "\\|").replace("\n", " ").strip()


def _code(text: str) -> str:
    return f"`{_md_escape_cell(text)}`" if text else ""


def _md_signature(sig: str) -> str:
    return f"```go\n{sig}\n```\n\n"


def _md_table(headers: list[str], rows: list[list[str]]) -> str:
    if not rows:
        return ""
    out = "| " + " | ".join(headers) + " |\n"
    out += "| " + " | ".join("---" for _ in headers) + " |\n"
    for row in rows:
        out += "| " + " | ".join(row) + " |\n"
    return out + "\n"


def _md_fields_table(fields: list[Field]) -> str:
    return _md_table(
        ["Field", "Type", "Description"],
        [[_code(f.name), _code(f.type_str), _md_escape_cell(_first_line(f.docs))] for f in fields],
    )


def _md_values_table(values: list[Value]) -> str:
    rows: list[list[str]] = []
    for v in values:
        rows.append([_code(", ".join(v.names)), _md_escape_cell(_first_line(v.docs))])
    return _md_table(["Name", "Description"], rows)


def _render_value(v: Value, level: int, depth: int) -> str:
    h = "#" * level
    out = f"\n{h} {', '.join(v.names)}\n\n"
    if v.decl:
        out += _md_signature(v.decl)
    body = _normalize_docs(v.docs, depth, level)
    if body:
        out += body + "\n\n"
    return out


def _render_funcs_detail(funcs: list[Func], level: int, examples: dict[str, str]) -> str:
    h = "#" * level
    out = ""
    for f in funcs:
        out += f"\n{h} {f.name}\n\n"
        out += _md_signature(f.signature)
        body = _normalize_docs(f.docs, _API_REFERENCE_DEPTH, level)
        if body:
            out += body + "\n\n"
        example = examples.get(f.name)
        # The facade generator already inlines the mapped example into each
        # method's doc comment, so only append one when the prose lacks it.
        if example and "```go" not in body:
            out += f"**Example**\n\n```go\n{example}\n```\n\n"
    return out


def _render_type_section(t: TypeItem, level: int, examples: dict[str, str]) -> str:
    h = "#" * level
    out = f"\n{h} {t.name}\n\n"
    if t.decl:
        out += _md_signature(t.decl)
    body = _normalize_docs(t.docs, _API_REFERENCE_DEPTH, level)
    if body:
        out += body + "\n\n"
    if t.fields:
        out += f"{h}# Fields\n\n" + _md_fields_table(t.fields)
    if t.consts:
        out += f"{h}# Constants\n\n"
        for c in t.consts:
            if c.decl:
                out += _md_signature(c.decl)
            cbody = _normalize_docs(c.docs, _API_REFERENCE_DEPTH, level + 1)
            if cbody:
                out += cbody + "\n\n"
    if t.funcs:
        out += f"{h}# Functions\n"
        out += _render_funcs_detail(t.funcs, level + 2, examples)
    if t.methods:
        out += f"{h}# Methods\n"
        out += _render_funcs_detail(t.methods, level + 2, examples)
    return out


# ---------------------------------------------------------------------------
# API reference page generators
# ---------------------------------------------------------------------------

# Bucket definition: every exported type in the root package is assigned to
# exactly one API reference page.
CLIENT_TYPES = ["CamundaClient"]
CONFIGURATION_TYPES = [
    "Config",
    "ConfigField",
    "Option",
    "AuthStrategy",
    "TLSConfig",
    "RetryConfig",
    "WorkerDefaults",
    "BackpressureProfile",
    "LogLevel",
]
WORKER_TYPES = [
    "JobWorker",
    "StreamJobWorker",
    "WorkerOption",
    "StreamWorkerOption",
    "Job",
    "JobHandler",
]
RUNTIME_TYPES = ["APIError", "BpmnError", "PollOption"]

# Types declared alongside the domain keys that are serialization plumbing, not
# identifiers. Everything else in domain-keys.json is published as a key, so a
# newly generated key type appears on the page without any change here.
NON_KEY_TYPES = {"ModelString", "NullableModelString", "NullableResourceKey"}

# Package-level var groups, keyed by their first declared name.
VAR_BUCKETS = {
    "ConfigSchema": "configuration",
    "ErrConfig": "runtime",
}

BUCKETS: dict[str, list[str]] = {
    "camunda-client": CLIENT_TYPES,
    "configuration": CONFIGURATION_TYPES,
    "job-workers": WORKER_TYPES,
    "runtime": RUNTIME_TYPES,
}


def classify_types(types: dict[str, TypeItem]) -> dict[str, list[TypeItem]]:
    """Assign every SDK type to exactly one API reference page.

    Fails loudly on an unclassified type so that a new exported type cannot be
    silently dropped from the published reference.
    """
    assigned: dict[str, list[TypeItem]] = {}
    claimed: set[str] = set()
    missing: list[str] = []
    for slug, names in BUCKETS.items():
        items: list[TypeItem] = []
        for n in names:
            t = types.get(n)
            if t is None:
                missing.append(n)
                continue
            items.append(t)
            claimed.add(n)
        assigned[slug] = items

    if missing:
        raise SystemExit(
            "Types listed in a bucket but absent from the SDK package:\n"
            + "\n".join(f"  - {n}" for n in missing)
            + "\n\nRemove each one from its bucket in generate-docusaurus-md.py, or "
            "restore the export."
        )

    unclassified = sorted(set(types) - claimed)
    if unclassified:
        raise SystemExit(
            "Unclassified exported types found in the SDK package:\n"
            + "\n".join(f"  - {n}" for n in unclassified)
            + "\n\nAdd each one to a bucket in generate-docusaurus-md.py so it appears "
            "in the published API reference (or confirm it should not be exported)."
        )
    return assigned


def classify_vars(values: list[Value]) -> dict[str, list[Value]]:
    """Route package-level var groups to their reference page."""
    out: dict[str, list[Value]] = {slug: [] for slug in BUCKETS}
    for v in values:
        head = v.names[0] if v.names else ""
        slug = VAR_BUCKETS.get(head)
        if slug is None:
            raise SystemExit(
                f"Unclassified package-level var group '{head}'.\n"
                "Add it to VAR_BUCKETS in generate-docusaurus-md.py."
            )
        out[slug].append(v)
    return out


def generate_camunda_client(
    types: list[TypeItem], examples: dict[str, str], import_path: str
) -> str:
    client = next((t for t in types if t.name == "CamundaClient"), None)
    out = frontmatter("CamundaClient", "CamundaClient")
    out += "\n# CamundaClient\n\n"
    if client is None:
        return out
    body = _normalize_docs(client.docs, _API_REFERENCE_DEPTH, 1)
    if body:
        out += body + "\n\n"
    out += (
        f"`CamundaClient` exposes **{len(client.methods)}** methods covering the full "
        "Orchestration Cluster REST API surface, with authentication, retries, and "
        "backpressure applied automatically.\n\n"
    )
    out += f"```go\nimport camunda \"{import_path}\"\n```\n\n"
    if client.funcs:
        out += "## Constructors\n"
        out += _render_funcs_detail(client.funcs, 3, examples)
    out += "## Methods\n\n"
    out += _md_table(
        ["Method", "Description"],
        [
            [f"[`{m.name}`](#{_slugify(m.name)})", _md_escape_cell(_first_line(m.docs))]
            for m in client.methods
        ],
    )
    out += "## Method details\n"
    out += _render_funcs_detail(client.methods, 3, examples)
    return out


def generate_section_page(
    title: str,
    intro: str,
    types: list[TypeItem],
    values: list[Value],
    funcs: list[Func],
    examples: dict[str, str],
) -> str:
    out = frontmatter(title, title)
    out += f"\n# {title}\n\n"
    if intro:
        out += intro + "\n\n"
    for t in sorted(types, key=lambda t: t.name):
        out += _render_type_section(t, 2, examples)
    if funcs:
        out += "\n## Package functions\n"
        out += _render_funcs_detail(funcs, 3, examples)
    for v in values:
        out += _render_value(v, 2, _API_REFERENCE_DEPTH)
    return out


def generate_domain_keys(keys: list[TypeItem]) -> str:
    out = frontmatter("Domain keys", "Domain keys")
    out += "\n# Domain keys\n\n"
    out += (
        "The Camunda Domain Type System replaces the bare `string` identifiers emitted "
        "by the OpenAPI generator with validated named types. Passing a "
        "`ProcessInstanceKey` where a `JobKey` is expected is a compile error, so whole "
        "classes of identifier mix-ups are caught before the request is sent.\n\n"
    )
    out += (
        '```go\nimport openapi "github.com/camunda/orchestration-cluster-api-go/client"'
        "\n```\n\n"
    )

    # Derive the shared constructor surface from a representative key rather
    # than hardcoding it, so the page cannot drift from the generated code.
    exemplar = max(keys, key=lambda k: len(k.funcs) + len(k.methods), default=None)
    if exemplar is not None:
        out += (
            f"Every key type exposes the same surface, shown here for "
            f"`{exemplar.name}`:\n\n"
        )
        out += _md_table(
            ["Function or method", "Description"],
            [
                [_code(f.signature.removeprefix("func ")), _md_escape_cell(_first_line(f.docs))]
                for f in exemplar.funcs + exemplar.methods
            ],
        )

    out += "## Key types\n\n"
    out += _md_table(
        ["Key type", "Underlying", "Description"],
        [
            [
                _code(k.name),
                _code(k.decl.split(" ", 2)[2] if k.decl.count(" ") >= 2 else "string"),
                _key_summary(k),
            ]
            for k in sorted(keys, key=lambda k: k.name)
        ],
    )
    out += (
        "Each key also has a matching `<Key>ExactMatch` wrapper used by the search "
        "filter models to express an exact-value match.\n"
    )
    return out


# The generator gives every key the same doc comment, which only restates the
# constructor surface already documented above the table.
_BOILERPLATE_KEY_DOC_RE = re.compile(
    r"^\w+ is a Camunda semantic key\.\s+Construct it with ", re.DOTALL
)


def _key_summary(k: TypeItem) -> str:
    summary = k.summary
    if not summary or _BOILERPLATE_KEY_DOC_RE.match(summary):
        return _fallback_key_summary(k.name)
    return _md_escape_cell(summary)


def _fallback_key_summary(name: str) -> str:
    """Description derived from the key's name when its doc comment says nothing."""
    for suffix, template in (
        ("Key", "Identifier for {article} {subject}."),
        ("Id", "Identifier for {article} {subject}."),
        ("Cursor", "Pagination cursor marking the {subject} of a result page."),
        ("Name", "Name of {article} {subject}."),
    ):
        if name.endswith(suffix) and name != suffix:
            subject = _humanize(name.removesuffix(suffix))
            if not subject:
                break
            article = "an" if subject[0] in "aeio" else "a"
            return template.format(article=article, subject=subject)
    return f"Validated {_humanize(name) or 'identifier'} value."


def _humanize(name: str) -> str:
    words = re.findall(r"[A-Z]+(?![a-z])|[A-Z][a-z0-9]*|[a-z0-9]+", name)
    return " ".join(w.lower() for w in words)


def generate_index(counts: dict[str, int]) -> str:
    out = frontmatter("API reference", "Overview")
    out += "\n# API reference\n\n"
    out += (
        "This reference covers the hand-written ergonomic surface of the Go SDK: the "
        "client, its configuration, the job workers, and the error and polling "
        "helpers.\n\n"
    )
    out += _md_table(
        ["Page", "Contents"],
        [
            [
                "[CamundaClient](camunda-client.md)",
                f"The client and its {counts.get('methods', 0)} API methods.",
            ],
            [
                "[Configuration](configuration.md)",
                "Client configuration, authentication, TLS, and retry policy.",
            ],
            [
                "[Job workers](job-workers.md)",
                "REST and gRPC job workers, their options, and the handler contract.",
            ],
            [
                "[Runtime](runtime.md)",
                "Error types, error classification, and eventual-consistency polling.",
            ],
            [
                "[Domain keys](domain-keys.md)",
                f"{counts.get('keys', 0)} validated identifier types.",
            ],
        ],
    )
    out += (
        "The generated request and response models are not reproduced here — there are "
        f"several hundred of them. Browse them on [pkg.go.dev]({PKG_GO_DEV_CLIENT}), or "
        "use your editor's go-to-definition on any method signature.\n"
    )
    return out


# ---------------------------------------------------------------------------
# Landing page + section page generator (from README.md)
# ---------------------------------------------------------------------------


def _strip_cut_sections(content: str) -> str:
    return re.sub(
        r"<!-- docs:cut:start -->.*?<!-- docs:cut:end -->\n?",
        "",
        content,
        flags=re.DOTALL,
    )


def _strip_snippet_markers(content: str) -> str:
    """Remove the `<!-- snippet-source: ... -->` provenance comments."""
    return re.sub(r"^<!-- snippet-source:.*?-->\n", "", content, flags=re.MULTILINE)


def _strip_contributing(content: str) -> str:
    return re.sub(r"\n## Contributing\b.*", "", content, flags=re.DOTALL)


def _clean_empty_lines(content: str) -> str:
    return re.sub(r"\n{4,}", "\n\n\n", content)


def _slugify(title: str) -> str:
    # Underscores are preserved to stay compatible with github-slugger, which
    # Docusaurus uses to derive heading anchors.
    slug = title.lower()
    slug = re.sub(r"[^a-z0-9\s_-]", "", slug)
    slug = re.sub(r"\s+", "-", slug.strip())
    slug = re.sub(r"-+", "-", slug)
    return slug


def _build_anchor_map(sections: list[tuple[str, str]]) -> dict[str, str]:
    anchor_to_page: dict[str, str] = {}
    for title, body in sections:
        page_slug = _slugify(title)
        for m in re.finditer(r"^#{2,6}\s+(.+)$", body, re.MULTILINE):
            anchor_to_page[_slugify(m.group(1).strip())] = page_slug
    return anchor_to_page


def _rewrite_internal_anchors(
    content: str, current_slug: str, anchor_map: dict[str, str]
) -> str:
    def _replace(m: re.Match) -> str:  # type: ignore[type-arg]
        text, anchor = m.group(1), m.group(2)
        target_page = anchor_map.get(anchor)
        if target_page and target_page != current_slug:
            return f"[{text}]({target_page}.md#{anchor})"
        return m.group(0)

    return re.sub(r"\[([^\]]+)\]\(#([^)]+)\)", _replace, content)


def _promote_headings(content: str) -> str:
    def _promote(m: re.Match) -> str:  # type: ignore[type-arg]
        hashes, rest = m.group(1), m.group(2)
        if len(hashes) > 1:
            return f"{'#' * (len(hashes) - 1)} {rest}"
        return m.group(0)

    return re.sub(r"^(#{1,6}) (.+)$", _promote, content, flags=re.MULTILINE)


def _split_by_h2(content: str) -> tuple[str, list[tuple[str, str]]]:
    parts = re.split(r"(?=^## )", content, flags=re.MULTILINE)
    preamble = parts[0]
    sections: list[tuple[str, str]] = []
    for part in parts[1:]:
        h2_match = re.match(r"^## (.+)\n", part)
        if h2_match:
            sections.append((h2_match.group(1).strip(), part))
    return preamble, sections


def generate_readme_pages(readme_path: Path, output_dir: Path) -> None:
    content = readme_path.read_text(encoding="utf-8")
    content = _strip_cut_sections(content)
    content = _strip_contributing(content)
    content = _strip_snippet_markers(content)
    content = _clean_empty_lines(content)

    content = re.sub(
        r"^#\s+.*$",
        "# Go SDK (Technical Preview)",
        content,
        count=1,
        flags=re.MULTILINE,
    )
    # Badge images point at shields.io / pkg.go.dev and add no value in the docs site.
    content = re.sub(r"^\[!\[.*?\)$\n?", "", content, flags=re.MULTILINE)
    # The README's own Technical Preview blockquote is replaced by the admonition.
    content = re.sub(
        r"^> \*\*Technical Preview\.\*\*.*?(?=\n\n)", "", content, flags=re.DOTALL | re.MULTILINE
    )
    content = content.strip() + "\n"

    preamble, sections = _split_by_h2(content)
    anchor_map = _build_anchor_map(sections)

    # --- Landing page: sibling of the section directory ---
    landing = _rewrite_docs_links(preamble, depth=_LANDING_PAGE_DEPTH)
    landing = _rewrite_cross_page_anchors(landing, anchor_map, prefix="go-sdk/")
    landing = rewrite_repo_links(landing)
    landing = inject_tech_preview_banner(landing)
    output_dir.mkdir(parents=True, exist_ok=True)
    landing_path = output_dir / "go-sdk.md"
    landing_path.write_text(
        LANDING_FRONTMATTER + _clean_empty_lines(landing).strip() + "\n", encoding="utf-8"
    )
    print(f"  Wrote landing page {landing_path}")

    # --- Section pages: one per H2 ---
    section_dir = output_dir / "go-sdk"
    section_dir.mkdir(parents=True, exist_ok=True)

    for i, (title, body) in enumerate(sections):
        slug = _slugify(title)
        position = i + 2  # landing page is 1
        section_content = _rewrite_docs_links(body, depth=_SECTION_PAGE_DEPTH)
        section_content = _rewrite_internal_anchors(section_content, slug, anchor_map)
        section_content = rewrite_repo_links(section_content)
        page_content = _promote_headings(section_content)
        page_content = inject_tech_preview_banner(page_content)
        fm = _make_section_frontmatter(slug, title, position)
        page_path = section_dir / f"{slug}.md"
        page_path.write_text(
            fm + _clean_empty_lines(page_content).strip() + "\n", encoding="utf-8"
        )
        print(f"  Wrote section {page_path}")

    # --- API Reference category metadata ---
    api_ref_dir = section_dir / "api-reference"
    if api_ref_dir.is_dir():
        category_path = api_ref_dir / "_category_.json"
        category_path.write_text(
            json.dumps({"label": "API Reference", "position": _API_REFERENCE_POSITION}, indent=2)
            + "\n",
            encoding="utf-8",
        )
        print(f"  Wrote {category_path}")


def _rewrite_cross_page_anchors(
    content: str, anchor_map: dict[str, str], prefix: str
) -> str:
    """Point the landing page's in-README anchors at the section pages."""

    def _replace(m: re.Match) -> str:  # type: ignore[type-arg]
        text, anchor = m.group(1), m.group(2)
        target_page = anchor_map.get(anchor)
        if target_page:
            return f"[{text}]({prefix}{target_page}.md#{anchor})"
        return m.group(0)

    return re.sub(r"\[([^\]]+)\]\(#([^)]+)\)", _replace, content)


# ---------------------------------------------------------------------------
# Link validation
# ---------------------------------------------------------------------------

_RELATIVE_LINK_RE = re.compile(r"\[([^\]]*)\]\((?!https?://|#|mailto:)([^)]+)\)")
# A bracket pair that no Markdown construct follows: not `](`, not `][`.
_BARE_BRACKET_RE = re.compile(r"\[([^\[\]]*)\](?![(\[])")
# `]:` opens a reference definition only at the start of a line.
_REF_DEF_RE = re.compile(r"^ {0,3}(\[)[^\[\]]*\]:")
_INLINE_CODE_RE = re.compile(r"`+[^`]*`+")
_TASK_LIST_RE = re.compile(r"^\s*[-*+]\s+\[[ xX]\]")
# Conservative: a bare (non-code-span) candidate must look like a Go symbol.
_GO_SYMBOL_RE = re.compile(r"\*?(?:\w+\.)?[A-Z]\w*(?:\.\w+)?")
_MASK = "\x00"


def _mask_inline_code(line: str) -> str:
    """Blank out inline code spans, preserving offsets so slices still line up."""
    return _INLINE_CODE_RE.sub(lambda m: _MASK * len(m.group(0)), line)


def _find_doc_links(line: str) -> list[str]:
    """Find Go doc links, which Markdown renders as literal brackets."""
    if _TASK_LIST_RE.match(line):
        return []
    masked = _mask_inline_code(line)
    ref_def = _REF_DEF_RE.match(masked)
    definition_start = ref_def.start(1) if ref_def else -1
    found: list[str] = []
    for m in _BARE_BRACKET_RE.finditer(masked):
        if m.start() == definition_start:
            continue
        inner_masked = m.group(1)
        inner = line[m.start(1) : m.end(1)]
        if _GO_SYMBOL_RE.fullmatch(inner_masked):
            found.append(f"[{inner}]")
    return found


def validate_generated_links(output_dir: Path) -> list[str]:
    """Flag links that will not resolve once copied into camunda-docs."""
    errors: list[str] = []
    for md_file in sorted(output_dir.rglob("*.md")):
        content = md_file.read_text(encoding="utf-8")
        in_fence = False
        for line_no, line in enumerate(content.splitlines(), start=1):
            if line.lstrip().startswith("```"):
                in_fence = not in_fence
                continue
            if in_fence:
                continue
            rel = md_file.relative_to(output_dir)
            for m in _RELATIVE_LINK_RE.finditer(line):
                target = m.group(2).split("#")[0]
                if not target or target.startswith("../") or "/" not in target:
                    continue
                errors.append(
                    f"  {rel}:{line_no}: repo-relative link [{m.group(1)}]({m.group(2)})"
                )
            for link in _find_doc_links(line):
                errors.append(f"  {rel}:{line_no}: unresolved Go doc link {link}")
    return errors


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------


def generate_api_reference() -> None:
    sdk = load_doc_json(SDK_JSON_PATH)
    buckets = classify_types(sdk.types)
    var_buckets = classify_vars(sdk.vars)
    examples = load_examples()

    OUTPUT_DIR.mkdir(parents=True, exist_ok=True)

    keys: list[TypeItem] = []
    if KEYS_JSON_PATH.is_file():
        key_pkg = load_doc_json(KEYS_JSON_PATH)
        keys = [t for t in key_pkg.types.values() if t.name not in NON_KEY_TYPES]
    else:
        print(f"  (no {KEYS_JSON_PATH.name}; skipping the domain keys page)")

    pages: list[tuple[str, str]] = [
        (
            "camunda-client.md",
            generate_camunda_client(buckets["camunda-client"], examples, sdk.import_path),
        ),
        (
            "configuration.md",
            generate_section_page(
                "Configuration",
                "Configuration is resolved from explicit options first, then environment "
                "variables, then built-in defaults, and validated fail-fast at "
                "construction.",
                buckets["configuration"],
                var_buckets["configuration"],
                [],
                examples,
            ),
        ),
        (
            "job-workers.md",
            generate_section_page(
                "Job workers",
                "Job workers obtain jobs of a given type — by polling the REST activation "
                "endpoint or over the gRPC job stream — run a handler, and report the "
                "outcome back to the cluster.",
                buckets["job-workers"],
                var_buckets["job-workers"],
                [],
                examples,
            ),
        ),
        (
            "runtime.md",
            generate_section_page(
                "Runtime",
                "Error types returned by every SDK call, the helpers that classify them, "
                "and the polling helper that absorbs eventual consistency.",
                buckets["runtime"],
                var_buckets["runtime"],
                sdk.funcs,
                examples,
            ),
        ),
    ]
    if keys:
        pages.append(("domain-keys.md", generate_domain_keys(keys)))

    client_type = sdk.types.get("CamundaClient")
    pages.insert(
        0,
        (
            "index.md",
            generate_index(
                {
                    "methods": len(client_type.methods) if client_type else 0,
                    "keys": len(keys),
                }
            ),
        ),
    )

    for filename, content in pages:
        path = OUTPUT_DIR / filename
        path.write_text(_clean_empty_lines(content).rstrip() + "\n", encoding="utf-8")
        print(f"  Wrote {path}")


def main() -> None:
    import argparse

    parser = argparse.ArgumentParser(
        description="Generate Docusaurus markdown from Go doc JSON + README"
    )
    parser.add_argument(
        "--validate-links",
        action="store_true",
        help="After generation, validate links in the generated markdown.",
    )
    parser.add_argument(
        "--readme-only",
        action="store_true",
        help="Only generate README section pages (skip the API reference).",
    )
    args = parser.parse_args()

    if not args.readme_only:
        print("Generating API reference from Go doc JSON...")
        generate_api_reference()

    print("Generating landing + section pages from README...")
    generate_readme_pages(README_PATH, DOCS_MD_DIR)

    if args.validate_links:
        print("Validating generated links...")
        errors = validate_generated_links(DOCS_MD_DIR)
        if errors:
            print("\nERROR: broken links found in generated markdown:")
            print("\n".join(errors))
            sys.exit(1)
        print("  All relative links OK.")

    print("Done.")


if __name__ == "__main__":
    main()
