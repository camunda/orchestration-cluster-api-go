package camunda

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	camundaapi "github.com/camunda/orchestration-cluster-api-go/v11/client"
	"github.com/camunda/orchestration-cluster-api-go/v11/pb"
)

// Discriminator values of the JobResult union on the wire.
const (
	jobResultTypeUserTask        = "userTask"
	jobResultTypeAdHocSubProcess = "adHocSubProcess"
)

// JobCompletion is the outcome of a successfully handled job: the variables to
// complete it with and, optionally, a job result.
//
// A job result is how a user task listener corrects or denies the task it
// listens to (see CorrectUserTask and DenyUserTask), and how the job of an
// ad-hoc sub-process tells the engine which elements to activate next (see
// AdHocSubProcessJobResult). Leave Result nil for an ordinary job.
type JobCompletion struct {
	Variables map[string]any
	Result    *JobResult
}

// JobCompletionHandler processes an activated job like a JobHandler, but
// completes it with a JobCompletion, which can carry a job result. Errors are
// handled exactly as for a JobHandler: a *BpmnError throws a BPMN error and any
// other error fails the job. Adapt it with CompletionHandler.
type JobCompletionHandler func(ctx context.Context, job *Job) (JobCompletion, error)

// CompletionHandler adapts a JobCompletionHandler to a JobHandler, so it can be
// passed to NewJobWorker or NewStreamJobWorker.
//
// The worker sets the result's "type" discriminator from the variant that is
// populated, so it is always correct on the wire. A result with no variant set,
// or with both, is a handler bug: the job is failed with a descriptive error
// rather than completed without the result the handler asked for.
func CompletionHandler(h JobCompletionHandler) JobHandler {
	return func(ctx context.Context, job *Job) (map[string]any, error) {
		c, err := h(ctx, job)
		if err != nil {
			return nil, err
		}
		job.result = c.Result
		return c.Variables, nil
	}
}

// UserTaskJobResult wraps a user task listener result as a JobResult with the
// "userTask" discriminator set.
func UserTaskJobResult(r JobResultUserTask) *JobResult {
	r.Type = ptrTo(jobResultTypeUserTask)
	return &JobResult{JobResultUserTask: &r}
}

// CorrectUserTask returns a user task listener result that corrects the task's
// attributes. Only the attributes set on c are corrected; the rest keep their
// persisted value. Clear the assignee with an empty string, the due or follow-up
// date with ClearDueDate or ClearFollowUpDate, and the candidate users or groups
// with an empty, non-nil slice.
func CorrectUserTask(c JobResultCorrections) *JobResult {
	r := camundaapi.NewJobResultUserTask()
	r.SetCorrections(c)
	return UserTaskJobResult(*r)
}

// DenyUserTask returns a user task listener result that denies the work, for
// example rejecting a task completion so the task stays active. reason is
// optional; an empty reason is omitted.
func DenyUserTask(reason string) *JobResult {
	r := camundaapi.NewJobResultUserTask()
	r.SetDenied(true)
	if reason != "" {
		r.SetDeniedReason(reason)
	}
	return UserTaskJobResult(*r)
}

// AdHocSubProcessJobResult wraps an ad-hoc sub-process result as a JobResult with
// the "adHocSubProcess" discriminator set.
func AdHocSubProcessJobResult(r JobResultAdHocSubProcess) *JobResult {
	r.Type = ptrTo(jobResultTypeAdHocSubProcess)
	return &JobResult{JobResultAdHocSubProcess: &r}
}

// ActivateAdHocElement returns an instruction to activate the element elementID
// inside an ad-hoc sub-process, creating variables (which may be nil) in the
// scope of the activated element.
func ActivateAdHocElement(elementID ElementId, variables map[string]any) JobResultActivateElement {
	e := camundaapi.NewJobResultActivateElement()
	e.SetElementId(elementID)
	if variables != nil {
		e.SetVariables(variables)
	}
	return *e
}

func ptrTo[T any](v T) *T { return &v }

// errInvalidJobResult reports a JobResult that cannot be put on the wire.
var errInvalidJobResult = errors.New("camunda: invalid job result")

// wireJobResult returns a copy of r with its "type" discriminator set from the
// populated variant. The generated union leaves the discriminator to the caller
// and the server defaults a missing one to "userTask", so an ad-hoc result sent
// as-is would be misread. A copy is taken so a result value shared between
// handlers is never written to concurrently.
func wireJobResult(r *JobResult) (*JobResult, error) {
	if r == nil {
		return nil, nil
	}
	switch {
	case r.JobResultUserTask != nil && r.JobResultAdHocSubProcess != nil:
		return nil, fmt.Errorf("%w: both the user task and the ad-hoc sub-process variant are set", errInvalidJobResult)
	case r.JobResultUserTask != nil:
		u := *r.JobResultUserTask
		u.Type = ptrTo(jobResultTypeUserTask)
		return &JobResult{JobResultUserTask: &u}, nil
	case r.JobResultAdHocSubProcess != nil:
		a := *r.JobResultAdHocSubProcess
		a.Type = ptrTo(jobResultTypeAdHocSubProcess)
		return &JobResult{JobResultAdHocSubProcess: &a}, nil
	default:
		return nil, fmt.Errorf("%w: no variant is set", errInvalidJobResult)
	}
}

// completionResult prepares the job result a CompletionHandler attached to job for
// sending alongside vars, or returns nil when there is none.
//
// A result-bearing completion is checked for JSON encodability up front: the
// result itself (a map holding math.NaN, a date outside years 0-9999) and the
// variables sent with it. Otherwise each transport would fail differently: the
// REST client errors before sending and the job is only logged, never failed, so
// it is redelivered after its timeout, while gRPC would fail it. An error wraps
// errInvalidJobResult and takes the job-failure path on every transport.
func completionResult(job *Job, vars map[string]any) (*JobResult, error) {
	r, err := wireJobResult(job.result)
	if r == nil || err != nil {
		return r, err
	}
	if _, err := json.Marshal(r); err != nil {
		return nil, fmt.Errorf("%w: %v", errInvalidJobResult, err)
	}
	if _, err := json.Marshal(vars); err != nil {
		return nil, fmt.Errorf("%w: completion variables: %v", errInvalidJobResult, err)
	}
	return r, nil
}

// grpcJobResult converts a JobResult to its gateway.proto form. Explicit nulls in
// the REST model mean "preserve the persisted value", as does omission, so both
// map to an unset proto field.
func grpcJobResult(r *JobResult) (*pb.JobResult, error) {
	r, err := wireJobResult(r)
	if r == nil || err != nil {
		return nil, err
	}
	if u := r.JobResultUserTask; u != nil {
		out := &pb.JobResult{Type: ptrTo(jobResultTypeUserTask)}
		if v, ok := u.GetDeniedOk(); ok && v != nil {
			out.Denied = ptrTo(*v)
		}
		if v, ok := u.GetDeniedReasonOk(); ok && v != nil {
			out.DeniedReason = ptrTo(*v)
		}
		if c, ok := u.GetCorrectionsOk(); ok && c != nil {
			out.Corrections = grpcCorrections(c)
		}
		return out, nil
	}
	a := r.JobResultAdHocSubProcess
	out := &pb.JobResult{Type: ptrTo(jobResultTypeAdHocSubProcess)}
	for _, e := range a.ActivateElements {
		el := &pb.JobResultActivateElement{ElementId: string(e.GetElementId())}
		if e.Variables != nil {
			b, err := json.Marshal(e.Variables)
			if err != nil {
				return nil, fmt.Errorf("%w: activate element %q variables: %v", errInvalidJobResult, el.ElementId, err)
			}
			el.Variables = string(b)
		}
		out.ActivateElements = append(out.ActivateElements, el)
	}
	if a.IsCompletionConditionFulfilled != nil {
		out.IsCompletionConditionFulfilled = ptrTo(*a.IsCompletionConditionFulfilled)
	}
	if a.IsCancelRemainingInstances != nil {
		out.IsCancelRemainingInstances = ptrTo(*a.IsCancelRemainingInstances)
	}
	return out, nil
}

func grpcCorrections(c *JobResultCorrections) *pb.JobResultCorrections {
	out := &pb.JobResultCorrections{}
	if v, ok := c.GetAssigneeOk(); ok && v != nil {
		out.Assignee = ptrTo(*v)
	}
	// An empty string clears a date, matching the REST wire form.
	if c.IsDueDateCleared() {
		out.DueDate = ptrTo("")
	} else if v, ok := c.GetDueDateOk(); ok && v != nil {
		out.DueDate = ptrTo(v.Format(time.RFC3339Nano))
	}
	if c.IsFollowUpDateCleared() {
		out.FollowUpDate = ptrTo("")
	} else if v, ok := c.GetFollowUpDateOk(); ok && v != nil {
		out.FollowUpDate = ptrTo(v.Format(time.RFC3339Nano))
	}
	// A non-nil empty slice clears the list, so only nil means "not corrected".
	if c.CandidateUsers != nil {
		out.CandidateUsers = &pb.StringList{Values: c.CandidateUsers}
	}
	if c.CandidateGroups != nil {
		out.CandidateGroups = &pb.StringList{Values: c.CandidateGroups}
	}
	if v, ok := c.GetPriorityOk(); ok && v != nil {
		out.Priority = ptrTo(*v)
	}
	return out
}
