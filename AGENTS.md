# AGENTS.md

> This repo follows the central Camunda AGENTS guidelines:
> https://raw.githubusercontent.com/camunda/.github/refs/heads/main/AGENTS.md
> The instructions below extend those and take precedence on conflict.

<!-- sdk-infra-delegation:start -->
> **Note:** This file also delegates to the shared SDK AGENTS.md. Read and apply it too.

**URL:**
https://raw.githubusercontent.com/camunda/sdk-infra/refs/heads/main/policies/AGENTS.md

It holds the conventions shared by all Camunda SDK repos (commits, bug fixes, tests, opening issues). Precedence on conflict: this file, then the shared SDK file, then the central Camunda file.
<!-- sdk-infra-delegation:end -->

## Opening issues

Every issue you open (`gh issue create`, REST or MCP) must carry the SDK defect-metrics fields (https://camunda.github.io/sdk-quality-metrics/). This applies in this repo and to issues you file elsewhere (other SDKs, camunda/sdk-infra, camunda/camunda). Put these lines at the end of the body, or set the matching labels directly:

```
Type: bug | enhancement | documentation | question
Found-by: review | ci | conformance-test | performance-test | integration | manual-test | audit | user
Affects: main | stable | both
Severity: critical | high | medium | low
Introduced-by: <commit sha | owner/repo@sha | #PR | release tag>   (optional)
```

- For a bug, `Found-by`, `Affects` and `Severity` are required. Without them the issue gets `needs-triage`.
- `Found-by` is the quality layer that caught it, not who typed the issue. `Affects: stable` means a released `stable/N` line.
- An issue created with a workflow's `GITHUB_TOKEN` does not trigger `sdk-issue-labels.yml`, so the body lines are not turned into labels. Set the labels directly instead: `--label bug --label found-by/<x> --label affects/<main|stable> --label severity/<x>`.

Value definitions: [shared policy, "Opening issues"](https://github.com/camunda/sdk-infra/blob/main/policies/AGENTS.md#opening-issues).

## Role & boundary

This repo produces the **Go SDK** for the Camunda 8 Orchestration Cluster REST API,
plus a **gRPC job-streaming worker**. It is a sibling of the JS / Python / C# / Rust
SDKs and follows the same pattern: a generated low-level client wrapped by a
hand-written ergonomic runtime. Target API version: **8.11** (`main`).

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
| `reexport_generated.go` | **Generated** by `./cmd/facadegen`. Re-exports every `client/` name except the HTTP-client plumbing (`plumbingFiles`/`plumbingNames` in `cmd/facadegen/reexport.go`), so callers import only `camunda`. Name client types by their `camunda` name in exported API. Never hand-edit. |
| `internal/config`, `internal/auth`, `internal/backpressure`, `internal/retry`, `internal/eventual`, `internal/transport`, `internal/diag` | Hand-written runtime. Cross-cutting concerns as an `http.RoundTripper` chain. Primary edit surface. |
| `internal/worker` | REST job worker + gRPC streaming worker. |
| `client/` | **Generated.** REST client from `openapi-generator`. Never hand-edit. |
| `pb/` | **Generated.** gRPC stubs from `buf`. Never hand-edit. |
| `cmd/facadegen/` | The AST-based facade and re-export generator. Primary edit surface for facade and re-export output. |
| `tools/apicompat/` | Stable-branch API compatibility checker (separate module, so its `golang.org/x/exp` dependency never reaches SDK consumers). |
| `scripts/generate.sh` | Pipeline orchestrator: bundle → fetch-proto → normalize-spec → openapi-generator → buf → post-process → gofmt → build. |
| `scripts/bundle-spec.sh`, `scripts/fetch-proto.sh` | Fetch the OpenAPI spec and `gateway.proto` (pinned to `$SPEC_REF`). |
| `scripts/postprocess.py`, `scripts/hooks/` | Numbered post-processing hooks: Domain Type System, semantic field types, generator-quirk fixes, version-skew tolerance, clearable date fields (`x-clearable`, see `scripts/normalize-spec.py`), then the facade (delegates to `cmd/facadegen`). Primary edit surface for fixing generated output. |
| `external-spec/bundled/` | Bundled spec (`rest-api.bundle.json`) + `spec-metadata.json`. Generator inputs. |
| `scripts/normalize-spec.py` | Rewrites properties typed by an `x-clearable` schema (`ClearableDateTime`) to a nullable `date-time` marked `x-clearable`, so they generate as `time.Time`; hook 09 then adds `Clear<Field>`. Writes the generator input `external-spec/generator/rest-api.json` (gitignored). |
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

`scripts/generate.sh` runs: `scripts/normalize-spec.py` → generator input; openapi-generator → `client/`; `buf generate` → `pb/`;
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

## Backporting to stable branches

`.github/workflows/backport.yml` calls camunda/sdk-infra's shared `sdk-backport.yml`
(`korthout/backport-action`, running as `camunda-sdk-automation`; the same flow as the other
SDKs). It is configured by `.github/backport.yml` and `.github/actions/codegen-setup`, both
read from the **target** branch, so keep them on `main` and on every live `stable/N`. It
opens cherry-pick PRs onto `stable/*` branches:

- Add the label `backport stable/<N>` to a PR. The backport PR opens when the PR merges,
  or right away if it is already merged.
- Or comment `/backport` on a merged PR to re-run it for its labels. PRs from forks can
  only be backported this way.
- Labelling an already-merged PR and `/backport` comments only work for people with write
  access.

Backport hand-written runtime, infra and generator changes (root package, `internal/**`,
`cmd/**`, `scripts/`) when the affected code exists on the stable branch. Never backport
generated output on its own (e.g. `chore(gen): regenerate for upstream spec drift`); each
branch regenerates from its own upstream spec.

On each backport branch the workflow adds up to two commits after the cherry-pick:

1. `chore: adapt backport of #N to stable/<N>`: the `post-cherry-pick` command rewrites
   the module path in the backported hand-written `.go`/`.md` files (`main` is `/v11`,
   `stable/10` is `/v10`). Don't fix imports by hand.
2. `chore(gen): regenerate for backport of #N`: the derived files (`generated-paths`:
   `external-spec/bundled`, `external-spec/proto`, `client/`, `pb/`, the root
   `*_generated.go`) are restored from the target branch and regenerated with
   `./scripts/generate.sh && go vet ./...`. PRs are squash-merged, so the cherry-pick
   carries `main`'s copies, but a stable branch must build from its own `SPEC_REF`
   (`stable/10` builds camunda `stable/8.10`). This also clears cherry-pick conflicts in
   those files. When the derived paths change, update `generated-paths` on every branch.

The generator runs in a job with a read-only token; a separate job pushes the commits only
if the adapt commit touches nothing but backported hand-written files and the
regeneration commit nothing but `generated-paths`.

If the cherry-pick conflicts, the action opens a **draft** PR with the conflicts committed,
and the workflow renames that commit to the source PR's title so it passes commitlint. It
stops at the first conflicting commit, so for a PR merged with a merge commit the draft can
be a **partial** backport. Resolve the conflicts on the backport branch, then comment
`/backport` on the source PR to adapt and regenerate it.

If adapting or regenerating fails, the bot comments on the backport PR. If the change
depends on spec the stable branch doesn't have (e.g. it wraps an operation that only
exists upstream on `main`, so the build fails), it can't be backported as-is: close the
backport PR, or reduce it to what applies to the stable spec.
## Versioning

Releases are automatic. On every push to `main` or `stable/**`, `release.yml` runs the
build/test gate and then semantic-release (`release.config.cjs`, extending the shared
`@camunda8/sdk-infra` base): `fix`/`perf`/`revert` → patch, `feat` → minor. It pushes
the `vX.Y.Z` tag, a `chore(release)` CHANGELOG.md commit and a GitHub Release as the
`camunda-sdk-automation` App (a ruleset bypass actor). There is no registry publish:
pkg.go.dev indexes the tag. Never push a release tag by hand.

- `main` publishes `-alpha.N` prereleases; `stable/N` publishes `N.x`. The current
  stable major comes from the `CAMUNDA_SDK_CURRENT_STABLE_MAJOR` repo variable.
- The SDK major tracks the Camunda server minor (server 8.10 → SDK v10.x), and Go
  carries majors ≥ 2 in the module path: `stable/10` is `/v10`, `main` is `/v11`.
  Pre-v10 releases (`v0.x`) used the unsuffixed path.
- `scripts/verify-release-major.sh` runs as the verifyRelease step and stops a release
  whose major disagrees with the module path. A `BREAKING CHANGE:` footer would cause
  exactly that, so CI's `breaking-change-guard` rejects it unless the PR carries the
  `breaking-change-approved` label.

### API compatibility on stable branches

`stable/N` regenerates from upstream `stable/8.N`, so spec drift can change the public
API with no one reviewing it (e.g. a field going from `T` to a `Nullable` wrapper).
`tools/apicompat` compares the public packages (not `internal/`, `cmd/`, `examples/`)
with the newest non-prerelease `vN.x.y` on proxy.golang.org, using `apidiff` in module
mode. Removed or changed types, funcs, methods, fields and constant values fail it, as
does a new method on an exported interface; additions pass. No `N.x` release yet passes
with a notice. Exit codes: 0 compatible, 1 breaking (each break listed), 2 setup error.

- `make api-compat` checks the working tree against the newest release of its module
  path's major (`API_COMPAT_ARGS="--baseline-version 10.0.0"` to pick one, or
  `--baseline-dir <checkout>` to compare with a local module).
- The `API compatibility` check (`api-compat.yml`) runs on PRs to `stable/**` and
  compares each PR with its base branch, so it answers only for its own changes. If a
  break is intended, label the PR `api-break-approved`: the check still lists the breaks
  but passes.
- `release.yml` compares the commit about to be tagged on `stable/**` with the newest
  release. It passes on the `api-break-approved` label of a PR merged into that branch
  since the baseline tag, but only for the API as of that PR's merge: a break added
  afterwards needs its own approval. A manual run with the `api_break_approved` input
  covers drift no PR introduced.

When `main` moves to a new server line, cut `stable/N` first, then on `main`:
`go mod edit -module` to the next `/vN`, rewrite the Go import strings, update the
`go_package` in `buf.gen.yaml`, and regenerate `pb/` with `buf generate` (the
descriptor embeds the path, so do not hand-edit it). Everything else reads the path
from `go.mod`; `TestModulePathLiteralsMatchGoMod` flags any literal that was missed.

semantic-release derives the next version from the last tag reachable on the branch,
so after the module-path move `main` must be seeded with the first prerelease of the
new major, or it computes the old major and the verify step rejects it. Tag the
module-path commit and attach the channel note semantic-release expects (on the
commit, not on an annotated tag object, or semantic-release does not see it):

```bash
git tag vN.0.0-alpha.1 <sha>
git notes --ref semantic-release-vN.0.0-alpha.1 add -m '{"channels":["alpha"]}' <sha>
git push origin vN.0.0-alpha.1 refs/notes/semantic-release-vN.0.0-alpha.1
gh release create vN.0.0-alpha.1 --prerelease --generate-notes --verify-tag
```

Also set `CAMUNDA_SDK_CURRENT_STABLE_MAJOR` to the newly cut stable major.

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
