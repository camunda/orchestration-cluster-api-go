package camunda

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	openapi "github.com/camunda/orchestration-cluster-api-go/client"
	"github.com/camunda/orchestration-cluster-api-go/pb"
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
func newRESTJob(aj openapi.ActivatedJobResult, clock Clock) *Job {
	return &Job{
		key:                string(aj.GetJobKey()),
		jobType:            aj.GetType(),
		retries:            aj.GetRetries(),
		processInstanceKey: string(aj.GetProcessInstanceKey()),
		elementID:          aj.GetElementId(),
		customHeaders:      aj.GetCustomHeaders(),
		variables:          aj.GetVariables(),
		leaseToken:         aj.GetLeaseToken(),
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
		leaseToken:         aj.GetLeaseToken(),
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
type JobHandler func(ctx context.Context, job *Job) (map[string]any, error)
