# AGENTS.md

> This repo follows the central Camunda AGENTS guidelines:
> https://raw.githubusercontent.com/camunda/.github/refs/heads/main/AGENTS.md
> The instructions below extend those and take precedence on conflict.

## Role & boundary

This repo produces the **Go SDK** for the Camunda 8 Orchestration Cluster REST API,
plus a **gRPC job-streaming worker**. It is a sibling of the JS / Python / C# / Rust
SDKs and follows the same pattern: a generated low-level client wrapped by a
hand-written ergonomic runtime. Target API version: **8.10** (`main`).

Upstream dependencies — fix at the source when they misbehave, not by working around
them here:

- [`camunda-schema-bundler`](https://github.com/camunda/camunda-schema-bundler) —
  fetches and bundles the upstream OpenAPI spec.
- [`openapi-generator`](https://github.com/OpenAPITools/openapi-generator) — generates
  `client/` (the `go` generator, net/http + encoding/json).
- [`buf`](https://buf.build) — compiles `gateway.proto` to Go + gRPC stubs in `pb/`.
- [`camunda/camunda`](https://github.com/camunda/camunda) — source of the OpenAPI spec
  and the Zeebe `gateway.proto`.

## Path map

| Path | Ownership and intent |
| --- | --- |
| `camunda.go`, `config.go`, `options.go`, `errors.go` | Public root package `camunda` — hand-written facade surface, configuration, options, error taxonomy. Primary edit surface. |
| `facade_generated.go` | **Generated** by `./cmd/facadegen` (AST-parses `client/`). One method per REST operation on `*CamundaClient`, routed through `guarded()`. Never hand-edit. |
| `internal/config`, `internal/auth`, `internal/backpressure`, `internal/retry`, `internal/eventual`, `internal/transport`, `internal/diag` | Hand-written runtime. Cross-cutting concerns as an `http.RoundTripper` chain. Primary edit surface. |
| `internal/worker` | REST job worker + gRPC streaming worker. |
| `client/` | **Generated.** REST client from `openapi-generator`. Never hand-edit. |
| `pb/` | **Generated.** gRPC stubs from `buf`. Never hand-edit. |
| `cmd/facadegen/` | The AST-based facade generator. Primary edit surface for facade output. |
| `scripts/generate.sh` | Pipeline orchestrator: bundle → fetch-proto → openapi-generator → buf → post-process → gofmt → build. |
| `scripts/bundle-spec.sh`, `scripts/fetch-proto.sh` | Fetch the OpenAPI spec and `gateway.proto` (pinned to `$SPEC_REF`). |
| `scripts/postprocess.py`, `scripts/hooks/` | Numbered post-processing hooks: Domain Type System, semantic field types, generator-quirk fixes, version-skew tolerance, then the facade (delegates to `cmd/facadegen`). Primary edit surface for fixing generated output. |
| `external-spec/bundled/` | Bundled spec (`rest-api.bundle.json`) + `spec-metadata.json`. Generator inputs. |
| `external-spec/proto/` | Fetched `gateway.proto`. Generator input. |
| `external-spec/upstream/` | Transient sparse clone. **Never commit** (gitignored). |
| `examples/` | Runnable examples; `readme.go` hosts region-tagged snippets injected into `README.md`. |

## The Camunda Domain Type System (important)

The spec marks identifier schemas as semantic keys (`JobKey`, `ProcessInstanceKey`, …).
`openapi-generator` emits these as bare `string`, losing the compile-time distinction
between key kinds. `scripts/hooks` replace them with validated named string types
(`type JobKey string` with `NewJobKey`/`MustJobKey` constructors and pattern validation)
and rewrite the affected struct fields. If you change how keys are represented, change it
there — not by hand-editing `client/`.

## gRPC job streaming (net-new vs the other SDKs)

Unlike the REST-only sibling SDKs, this SDK also exposes a **gRPC streaming job worker**
built on the Zeebe `gateway.proto` `StreamActivatedJobs` RPC. A streamed job is activated
over gRPC and completed over gRPC (`CompleteJob`/`FailJob`/`ThrowError`/`UpdateJobTimeout`);
all other operations use REST. The streaming worker keeps a low-frequency REST
`activate-jobs` **sidecar poll** as a safety net for re-queued jobs (mirrors the
`camunda-8-js-sdk` `ZBStreamWorker` design).

## Generation pipeline

```bash
make bundle       # re-bundle upstream OpenAPI spec (ref: $SPEC_REF) + regenerate
make fetch-proto  # fetch gateway.proto (ref: $SPEC_REF)
make generate     # regenerate REST client + gRPC stubs + post-process from the fetched inputs
```

`scripts/generate.sh` runs: openapi-generator → `client/`; `buf generate` → `pb/`;
`scripts/postprocess.py` (numbered hooks incl. `cmd/facadegen`); `gofmt -w`; `go build`.

## Build / test / lint

```bash
make build   # go build ./...
make test    # go test ./...
make vet      # go vet ./...
make lint     # golangci-lint (if installed) + buf lint
make fmt      # gofmt -w .
make examples # go build ./examples/...
make check    # full local gate: fmt-check vet build test examples
```

### Always-green policy

The hand-written packages (root + `internal/**` + `cmd/**`) must build, `go vet`
clean, `gofmt` clean, and pass `go test ./...`. Warnings are not suppressed to make a
build pass. Generated packages (`client/`, `pb/`, `facade_generated.go`) are produced
by the pipeline and not hand-edited.

CI (`.github/workflows/ci.yml`) runs this gate on every push and pull request, plus a
nightly scheduled run that re-bundles from upstream. The `Generation drift` job is
report-only: on drift it emits a warning and uploads a `generation-drift` patch artifact
for a follow-up regen PR, rather than failing the run — routine upstream spec churn must
not mask a real regression in the other jobs. It does still fail if the codegen pipeline
itself breaks.

## Separate generator changes from regenerated output

When a change modifies the generator (hooks, `cmd/facadegen`, configs, scripts) **and**
that change alters `client/`, `pb/`, or `facade_generated.go`, split into two commits:

1. Generator change only (scripts / hooks / facadegen / config). No generated changes.
2. `chore(gen): regenerate for <summary>` — the regenerated output.

This keeps cherry-picks clean and reviews readable, and preserves `git blame` on both
surfaces.

## Commit messages

Conventional Commits (enforced by commitlint via the shared `@camunda8/sdk-infra` base
config). Use `fix` only for user-facing bug fixes (triggers a release); use `chore` for
review-comment fix-ups and regeneration commits.

## Versioning

Semantic versioning via `git` tags. The SDK major tracks the Camunda server minor
(server 8.9 → SDK 9.x). Go majors ≥ 2 require the `/vN` module-path suffix; during
Technical Preview the module stays at `v0`.

## Go conventions

- `context.Context` is the first argument of every blocking/IO method.
- Configuration via functional options (`camunda.New(opts...)`); options override
  environment, which overrides defaults.
- Errors are values: typed `*APIError` (carries HTTP status + body) plus sentinel
  errors (`ErrConfig`, `ErrAuth`, …). Test with `errors.Is` / `errors.As`.
- No silent failure: a function that cannot honor its contract returns an error rather
  than a zero value or best-guess default.
- Bug fixes follow red/green: write the failing test first, scope it to the defect
  *class*, then apply the minimal fix.
