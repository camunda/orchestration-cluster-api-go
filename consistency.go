package camunda

// IsEventuallyConsistent reports whether the REST operation with the given
// operationId is eventually consistent: a read issued immediately after a
// related write may not observe the write yet. Wrap such reads in Poll to
// tolerate propagation delay.
//
// The operationId is the OpenAPI operation id (camelCase), e.g.
// "getProcessInstance". The set is generated from the spec metadata.
func IsEventuallyConsistent(operationID string) bool {
	return eventuallyConsistentOps[operationID]
}
