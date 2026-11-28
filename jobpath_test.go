package camunda

import "testing"

func TestJobCommandPathMatchesFinalSegmentsOnly(t *testing.T) {
	cases := map[string]bool{
		"/v2/jobs/123/completion":                  true,
		"/v2/jobs/123/failure":                     true,
		"/v2/jobs/123/error":                       true,
		"/jobs/proxy/v2/jobs/1/completion":         true,
		"/jobs/proxy/user-tasks/123/completion":    false,
		"/jobs/proxy/v2/user-tasks/123/completion": false,
		"/v2/jobs/activation":                      false,
		"/jobs/x/v2/incidents/1/error":             false,
		"/v2/jobs//completion":                     false,
	}
	for p, want := range cases {
		if got := isJobCommandPath(p); got != want {
			t.Errorf("isJobCommandPath(%q) = %v, want %v", p, got, want)
		}
	}
	if !isUserTaskCompletionPath("/jobs/proxy/user-tasks/123/completion") {
		t.Error("user-task completion behind /jobs proxy not matched")
	}
	if isUserTaskCompletionPath("/user-tasks/proxy/v2/jobs/1/completion") {
		t.Error("job completion behind /user-tasks proxy matched as user-task")
	}
}
