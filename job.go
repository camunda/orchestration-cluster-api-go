package camunda

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	camundaapi "github.com/camunda/orchestration-cluster-api-go/v11/client"
	"github.com/camunda/orchestration-cluster-api-go/v11/pb"
)

// Job is an activated job passed to a JobHandler. It is transport-agnostic: the
// same type is produced by the REST job worker and the gRPC streaming worker.
//
// # Variables are untrusted input
//
// A job's variables reach the handler through the cluster, but their content is
// set by whoever started the process instance. Treat them as you would the body
// of an HTTP request from an untrusted caller.
//
// What the SDK guarantees:
//
//   - Variables are decoded into JSON-shaped values only — a map[string]any,
//     via the generated model for REST jobs and encoding/json for gRPC jobs.
//     Nothing here can instantiate a type chosen by the payload.
//   - [Job.Variables] fills only the type the handler author supplied; the
//     payload selects values, never a type.
//   - Nesting is bounded by encoding/json's limit of 10000, so a deeply nested
//     payload cannot exhaust the stack. On a gRPC-streamed job the decode error
//     is swallowed and the handler sees no variables at all, so check for the
//     keys you need rather than expecting a decode failure to surface.
//   - Variable values are kept out of the SDK's own log records: the worker logs
//     job keys and types, never the variable map or a raw frame. A decoding
//     error is logged, though, and a Go JSON error can quote the offending
//     literal, so a malformed value can reach the log by that route.
//   - Each job owns its maps, so concurrent handlers never share or mutate the
//     same values.
//
// What the SDK does not do:
//
//   - It enforces no variable-specific size limit: whatever arrives is decoded.
//     Two nearby limits are not that limit — the FALCON command stream caps a
//     single frame at 16 MiB (a transport limit that does not apply to REST or
//     gRPC), and WithMaxConcurrentJobs / WithStreamMaxConcurrentJobs cap how
//     many handlers run at once, bounding neither the bytes in one payload nor
//     the jobs buffered before dispatch. Whether the SDK should impose a limit
//     of its own is an open question; none is applied today.
//   - It does not validate or sanitise values beyond what JSON decoding
//     requires, and it does not interpret them. A variable holding a path, URL,
//     query fragment or shell string is just a string here — if a handler
//     passes one to a filesystem, database, template or subprocess, enforcing
//     that trust boundary is the handler's job.
//
// [Job.RawVariables] is subject to all of the above: its content is untrusted
// input, and the SDK neither validates nor size-limits it. It returns the job's
// own decoded map, not a copy, so reading from it is safe, but copy it before
// handing it to code that may retain or mutate it.
type Job struct {
	key                string
	jobType            string
	retries            int32
	processInstanceKey string
	elementID          string
	customHeaders      map[string]any
	variables          map[string]any
	leaseToken         string
	clock              Clock
	// result is the job result a CompletionHandler returned, sent on completion.
	result *JobResult
}

// Key returns the job key.
func (j *Job) Key() string { return j.key }

// Type returns the job type.
func (j *Job) Type() string { return j.jobType }

// Retries returns the job's remaining retries.
func (j *Job) Retries() int32 { return j.retries }

// ProcessInstanceKey returns the key of the owning process instance.
func (j *Job) ProcessInstanceKey() string { return j.processInstanceKey }

// ElementID returns the BPMN element id that created the job.
func (j *Job) ElementID() string { return j.elementID }

// CustomHeaders returns the job's custom headers.
func (j *Job) CustomHeaders() map[string]any { return j.customHeaders }

// RawVariables returns the job variables as a decoded map.
func (j *Job) RawVariables() map[string]any { return j.variables }

// LeaseToken returns the activation lease token, or "" if the job was not leased.
func (j *Job) LeaseToken() string { return j.leaseToken }

// Clock returns the worker's clock. A handler that needs to wait should use this
// rather than time.Sleep, so an injected clock controls it.
//
// For short in-handler coordination only -- spacing a retry, waiting for a resource
// to settle. A long or business wait belongs in the process as a BPMN timer event: a
// handler holding a job for minutes occupies a worker slot, risks the job timeout
// expiring underneath it, and hides the wait from the process model where it cannot
// be seen or changed.
func (j *Job) Clock() Clock { return j.clock }

// Variables unmarshals the job variables into v (a pointer to a struct or map).
//
// v selects the target type, never the payload: the decoded variables are
// re-marshalled and unmarshalled into the type the handler author named, so a
// payload cannot cause an arbitrary type to be instantiated. The values are
// still untrusted input and are not validated or size-limited by the SDK; see
// [Job] for the full trust model.
func (j *Job) Variables(v any) error {
	b, err := json.Marshal(j.variables)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// newRESTJob builds a Job from a REST activate-jobs result. clock is positional so
// the compiler rejects a delivery path that forgets to pass the worker's clock.
func newRESTJob(aj camundaapi.ActivatedJobResult, clock Clock) *Job {
	return &Job{
		key:                string(aj.GetJobKey()),
		jobType:            aj.GetType(),
		retries:            aj.GetRetries(),
		processInstanceKey: string(aj.GetProcessInstanceKey()),
		elementID:          string(aj.GetElementId()),
		customHeaders:      aj.GetCustomHeaders(),
		variables:          aj.GetVariables(),
		leaseToken:         string(aj.GetJobLeaseToken()),
		clock:              clock,
	}
}

// newGRPCJob builds a Job from a gRPC-streamed ActivatedJob. The gRPC message
// carries variables and custom headers as JSON strings, which are decoded here.
func newGRPCJob(aj *pb.ActivatedJob, clock Clock) *Job {
	return &Job{
		key:                strconv.FormatInt(aj.GetKey(), 10),
		jobType:            aj.GetType(),
		retries:            aj.GetRetries(),
		processInstanceKey: strconv.FormatInt(aj.GetProcessInstanceKey(), 10),
		elementID:          aj.GetElementId(),
		customHeaders:      parseJSONObject(aj.GetCustomHeaders()),
		variables:          parseJSONObject(aj.GetVariables()),
		leaseToken:         aj.GetJobLeaseToken(),
		clock:              clock,
	}
}

func parseJSONObject(s string) map[string]any {
	if s == "" {
		return map[string]any{}
	}
	var m map[string]any
	if json.Unmarshal([]byte(s), &m) != nil {
		return map[string]any{}
	}
	return m
}

// BpmnError is an error that, when returned by a JobHandler, makes the worker throw
// a BPMN error (raising a catch event) instead of failing the job.
type BpmnError struct {
	Code      string
	Message   string
	Variables map[string]any
}

func (e *BpmnError) Error() string {
	return fmt.Sprintf("bpmn error %q: %s", e.Code, e.Message)
}

// JobHandler processes an activated job:
//   - returning (variables, nil) completes the job with those variables;
//   - returning a *BpmnError throws a BPMN error;
//   - returning any other error fails the job (decrementing its retries).
//
// To also complete the job with a job result (user task listener corrections or
// denial, or an ad-hoc sub-process result), write a JobCompletionHandler and adapt
// it with CompletionHandler.
type JobHandler func(ctx context.Context, job *Job) (map[string]any, error)
