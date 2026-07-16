package camunda_test

import (
	"testing"

	camunda "github.com/camunda/orchestration-cluster-api-go"
)

func TestIsEventuallyConsistent(t *testing.T) {
	// getAgentInstance is marked eventually consistent in the spec metadata.
	if !camunda.IsEventuallyConsistent("getAgentInstance") {
		t.Error("getAgentInstance should be eventually consistent")
	}
	// An unknown operation id is not eventually consistent.
	if camunda.IsEventuallyConsistent("nonexistentOperation") {
		t.Error("unknown operation should not be eventually consistent")
	}
}
