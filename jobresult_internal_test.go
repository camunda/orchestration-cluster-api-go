package camunda

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/camunda/orchestration-cluster-api-go/v11/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

// TestStreamJobWorkerSendsJobResultOverGRPC verifies that the gRPC streaming
// worker carries a CompletionHandler's job result on CompleteJob, with the
// discriminator set, and that a plain completion carries none.
func TestStreamJobWorkerSendsJobResultOverGRPC(t *testing.T) {
	lis := bufconn.Listen(1024 * 1024)
	fake := &fakeGateway{
		jobs: []*pb.ActivatedJob{
			{Key: 1, Type: "plain"},
			{Key: 2, Type: "correct"},
			{Key: 3, Type: "deny"},
			{Key: 4, Type: "adhoc"},
			{Key: 5, Type: "clear"},
		},
		completes: make(chan *pb.CompleteJobRequest, 5),
		fails:     make(chan *pb.FailJobRequest, 1),
		throws:    make(chan *pb.ThrowErrorRequest, 1),
	}
	srv := grpc.NewServer()
	pb.RegisterGatewayServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	client, err := New(WithNoAuth())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	corrections := NewJobResultCorrections()
	corrections.SetAssignee("corrected-user")
	corrections.SetPriority(80)
	corrections.SetDueDate(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	corrections.SetCandidateUsers([]string{})
	corrections.SetCandidateGroups([]string{"reviewers"})

	clearing := NewJobResultCorrections()
	clearing.ClearDueDate()
	clearing.SetFollowUpDate(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))

	adHoc := NewJobResultAdHocSubProcess()
	adHoc.SetActivateElements([]JobResultActivateElement{
		ActivateAdHocElement("task_a", map[string]any{"input": "x"}),
		ActivateAdHocElement("task_b", nil),
	})
	adHoc.SetIsCompletionConditionFulfilled(true)
	adHoc.SetIsCancelRemainingInstances(false)

	handler := CompletionHandler(func(_ context.Context, job *Job) (JobCompletion, error) {
		switch job.Type() {
		case "correct":
			return JobCompletion{Result: CorrectUserTask(*corrections)}, nil
		case "clear":
			return JobCompletion{Result: CorrectUserTask(*clearing)}, nil
		case "deny":
			return JobCompletion{Result: DenyUserTask("Insufficient documentation")}, nil
		case "adhoc":
			return JobCompletion{Variables: map[string]any{"round": 1}, Result: AdHocSubProcessJobResult(*adHoc)}, nil
		default:
			return JobCompletion{Variables: map[string]any{"ok": true}}, nil
		}
	})

	w := client.NewStreamJobWorker("any", handler, WithStreamPollInterval(-1), WithStreamMaxConcurrentJobs(5))
	w.dial = bufDial(lis)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	got := map[int64]*pb.CompleteJobRequest{}
	for range fake.jobs {
		req := waitFor(t, fake.completes)
		got[req.JobKey] = req
	}

	if got[1].Result != nil {
		t.Errorf("plain completion Result = %v, want nil", got[1].Result)
	}

	wantCorrect := &pb.JobResult{
		Type: ptrTo("userTask"),
		Corrections: &pb.JobResultCorrections{
			Assignee:        ptrTo("corrected-user"),
			DueDate:         ptrTo("2026-01-02T03:04:05Z"),
			CandidateUsers:  &pb.StringList{},
			CandidateGroups: &pb.StringList{Values: []string{"reviewers"}},
			Priority:        ptrTo(int32(80)),
		},
	}
	if !proto.Equal(got[2].Result, wantCorrect) {
		t.Errorf("corrections Result = %v, want %v", got[2].Result, wantCorrect)
	}

	wantDeny := &pb.JobResult{
		Type:         ptrTo("userTask"),
		Denied:       ptrTo(true),
		DeniedReason: ptrTo("Insufficient documentation"),
	}
	if !proto.Equal(got[3].Result, wantDeny) {
		t.Errorf("denial Result = %v, want %v", got[3].Result, wantDeny)
	}

	wantAdHoc := &pb.JobResult{
		Type: ptrTo("adHocSubProcess"),
		ActivateElements: []*pb.JobResultActivateElement{
			{ElementId: "task_a", Variables: `{"input":"x"}`},
			{ElementId: "task_b"},
		},
		IsCompletionConditionFulfilled: ptrTo(true),
		IsCancelRemainingInstances:     ptrTo(false),
	}
	if !proto.Equal(got[4].Result, wantAdHoc) {
		t.Errorf("ad-hoc Result = %v, want %v", got[4].Result, wantAdHoc)
	}
	wantClear := &pb.JobResult{
		Type: ptrTo("userTask"),
		Corrections: &pb.JobResultCorrections{
			DueDate:      ptrTo(""),
			FollowUpDate: ptrTo("2026-01-02T03:04:05Z"),
		},
	}
	if !proto.Equal(got[5].Result, wantClear) {
		t.Errorf("cleared-date Result = %v, want %v", got[5].Result, wantClear)
	}
	if got[4].Variables != `{"round":1}` {
		t.Errorf("ad-hoc Variables = %q, want {\"round\":1}", got[4].Variables)
	}
}

// TestStreamJobWorkerFailsJobOnInvalidResult mirrors the REST worker: an
// unsendable result fails the job rather than completing it without the result.
func TestStreamJobWorkerFailsJobOnInvalidResult(t *testing.T) {
	lis := bufconn.Listen(1024 * 1024)
	fake := &fakeGateway{
		jobs:      []*pb.ActivatedJob{{Key: 7, Type: "bad", Retries: 3}},
		completes: make(chan *pb.CompleteJobRequest, 1),
		fails:     make(chan *pb.FailJobRequest, 1),
		throws:    make(chan *pb.ThrowErrorRequest, 1),
	}
	srv := grpc.NewServer()
	pb.RegisterGatewayServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	client, err := New(WithNoAuth())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w := client.NewStreamJobWorker("bad", CompletionHandler(
		func(context.Context, *Job) (JobCompletion, error) {
			return JobCompletion{Result: &JobResult{}}, nil
		}), WithStreamPollInterval(-1))
	w.dial = bufDial(lis)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	fl := waitFor(t, fake.fails)
	if fl.JobKey != 7 || !strings.Contains(fl.ErrorMessage, "invalid job result") {
		t.Errorf("fail = {JobKey:%d ErrorMessage:%q}, want job 7 failed for an invalid job result", fl.JobKey, fl.ErrorMessage)
	}
}

// TestStreamJobWorkerFailsJobOnUnencodableResult mirrors the REST and FALCON
// workers: a result-bearing completion that cannot be JSON-encoded fails the job
// instead of completing it with its variables silently dropped.
func TestStreamJobWorkerFailsJobOnUnencodableResult(t *testing.T) {
	adHoc := NewJobResultAdHocSubProcess()
	adHoc.SetActivateElements([]JobResultActivateElement{
		ActivateAdHocElement("task_a", map[string]any{"score": math.NaN()}),
	})
	corrections := NewJobResultCorrections()
	corrections.SetDueDate(time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC))
	completions := map[string]JobCompletion{
		"adhoc":     {Result: AdHocSubProcessJobResult(*adHoc)},
		"corrected": {Result: CorrectUserTask(*corrections)},
		"variables": {Variables: map[string]any{"score": math.Inf(1)}, Result: DenyUserTask("nope")},
	}

	lis := bufconn.Listen(1024 * 1024)
	fake := &fakeGateway{
		jobs: []*pb.ActivatedJob{
			{Key: 1, Type: "adhoc", Retries: 3},
			{Key: 2, Type: "corrected", Retries: 3},
			{Key: 3, Type: "variables", Retries: 3},
		},
		completes: make(chan *pb.CompleteJobRequest, 3),
		fails:     make(chan *pb.FailJobRequest, 3),
		throws:    make(chan *pb.ThrowErrorRequest, 1),
	}
	srv := grpc.NewServer()
	pb.RegisterGatewayServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	client, err := New(WithNoAuth())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w := client.NewStreamJobWorker("any", CompletionHandler(
		func(_ context.Context, job *Job) (JobCompletion, error) {
			return completions[job.Type()], nil
		}), WithStreamPollInterval(-1), WithStreamMaxConcurrentJobs(3))
	w.dial = bufDial(lis)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	failed := map[int64]bool{}
	for range fake.jobs {
		select {
		case fl := <-fake.fails:
			if !strings.Contains(fl.ErrorMessage, "invalid job result") {
				t.Errorf("job %d failed with %q, want an invalid job result", fl.JobKey, fl.ErrorMessage)
			}
			failed[fl.JobKey] = true
		case comp := <-fake.completes:
			t.Errorf("job %d was completed: %v", comp.JobKey, comp)
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out; failed so far: %v", failed)
		}
	}
}

func TestWireJobResultRejectsAmbiguousUnion(t *testing.T) {
	_, err := wireJobResult(&JobResult{
		JobResultUserTask:        NewJobResultUserTask(),
		JobResultAdHocSubProcess: NewJobResultAdHocSubProcess(),
	})
	if !errors.Is(err, errInvalidJobResult) {
		t.Errorf("err = %v, want errInvalidJobResult", err)
	}
}
