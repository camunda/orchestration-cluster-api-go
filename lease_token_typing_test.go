package camunda

import (
	"testing"

	openapi "github.com/camunda/orchestration-cluster-api-go/client"
)

// TestLeasedActivatedJobTokenIsPlainString is a compile-time proof of the
// spec-driven ("x-present-when") leased projection: ActivateJobsWithLease returns
// jobs whose LeaseToken is a guaranteed-present string, while the raw
// ActivatedJobResult keeps a nullable token. If facadegen ever stops shadowing the
// embedded field with a plain string, this test stops compiling.
func TestLeasedActivatedJobTokenIsPlainString(t *testing.T) {
	// given a leased job produced by ActivateJobsWithLease
	var leased LeasedActivatedJobResult

	// then its lease token is a plain string, usable without a nil check
	var token string = leased.LeaseToken
	_ = token

	// and the embedded raw model still carries the nullable token, reachable
	// explicitly — the projection shadows it rather than dropping it.
	var raw openapi.NullableString = leased.ActivatedJobResult.LeaseToken
	_ = raw
}
