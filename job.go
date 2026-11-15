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

// CustomHeaders returns the job's custom headers. The map is never nil, so a
// handler may read from or write to it on any transport (see RawVariables).
func (j *Job) CustomHeaders() map[string]any { return j.customHeaders }

// RawVariables returns the job variables as a decoded map.
//
// The map is never nil: a job with no variables (absent, null or empty) yields an
// empty, writable map, identically on the REST and gRPC transports. Each job owns
// its map, so a handler may add to it freely without affecting other jobs.
//
// Malformed payloads are handled differently per transport. The gRPC stream
// carries variables as a JSON string, and one that cannot be decoded as an object
// also yields an empty map. On REST the response is decoded before any Job is
// built, so a malformed variables value fails the activation with an error and no
// job reaches the handler.
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
		customHeaders:      nonNilMap(aj.GetCustomHeaders()),
		variables:          nonNilMap(aj.GetVariables()),
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

// parseJSONObject decodes a JSON object into a map. It never returns nil: an empty
// string, a malformed or non-object payload, and the JSON literal null (which
// json.Unmarshal accepts but leaves the map nil) all yield an empty map.
func parseJSONObject(s string) map[string]any {
	if s == "" {
		return map[string]any{}
	}
	var m map[string]any
	if json.Unmarshal([]byte(s), &m) != nil {
		return map[string]any{}
	}
	return nonNilMap(m)
}

// nonNilMap returns m, or a fresh empty map when m is nil, so a Job never hands a
// handler a map it cannot write to.
func nonNilMap(m map[string]any) map[string]any {
	if m == nil {
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
