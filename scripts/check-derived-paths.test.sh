#!/usr/bin/env bash
# Self-test for scripts/check-derived-paths.sh (run in CI).
set -uo pipefail
here=$(cd "$(dirname "$0")" && pwd)
check="$here/check-derived-paths.sh"
fails=0

expect() { # expect <0|1> <paths...>
  local want=$1; shift
  local got=0
  printf '%s\0' "$@" | "$check" 2>/dev/null || got=$?
  if [ "$got" -ne "$want" ]; then
    echo "FAIL: want exit $want, got $got for: $*"
    fails=1
  fi
}

# Accepted: only generated paths.
expect 0 client/api_job.go
expect 0 pb/gateway.pb.go client/model_x.go
expect 0 external-spec/bundled/rest-api.bundle.json external-spec/proto/gateway.proto
expect 0 facade_generated.go reexport_generated.go consistency_generated.go presentwhen_generated.go
# Rejected: hand-written and generator code, the workflow itself.
expect 1 scripts/generate.sh
expect 1 .github/workflows/backport.yml
expect 1 camunda.go
expect 1 cmd/facadegen/main.go
expect 1 internal/worker/worker.go
expect 1 go.mod
# Rejected: one bad path poisons an otherwise valid patch.
expect 1 client/api_job.go scripts/generate.sh
# Rejected: prefix look-alikes, bare dirs, traversal, odd forms.
expect 1 client
expect 1 clientx/evil.go
expect 1 client.go
expect 1 pb
expect 1 facade_generated.go.bak
expect 1 x/facade_generated.go
expect 1 external-spec/upstream/x
expect 1 external-spec/bundled
expect 1 client/../scripts/generate.sh
expect 1 ../client/x.go
expect 1 /client/x.go
expect 1 ./client/x.go
expect 1 client/./x.go
expect 1 ""
# Empty input is a no-op patch: accepted.
if ! printf '' | "$check"; then echo "FAIL: empty input rejected"; fails=1; fi
# --list is the restore list used by the regeneration job.
want="external-spec/bundled external-spec/proto client pb facade_generated.go reexport_generated.go consistency_generated.go presentwhen_generated.go"
got=$("$check" --list)
[ "$got" = "$want" ] || { echo "FAIL: --list = '$got'"; fails=1; }
# Every gofmt target in generate.sh is allowlisted.
targets=$(sed -n 's/^GOFMT_TARGETS=(\(.*\))$/\1/p' "$here/generate.sh")
[ -n "$targets" ] || { echo "FAIL: could not read GOFMT_TARGETS"; fails=1; }
for t in $targets; do
  case " $got " in *" $t "*) ;; *) echo "FAIL: GOFMT_TARGETS entry '$t' not in allowlist"; fails=1 ;; esac
done

[ $fails -eq 0 ] && echo "check-derived-paths: all tests passed"
exit $fails
