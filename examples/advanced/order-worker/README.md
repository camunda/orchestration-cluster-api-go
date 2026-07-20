# Resilient inventory worker

This example deploys `order.bpmn` and processes three orders:

1. normal inventory reservation;
2. an out-of-stock business outcome;
3. a simulated inventory API timeout followed by a successful retry.

It demonstrates the most important worker rule: **business outcomes belong in
the process model; technical failures belong in job retries.**

## Run it

Start c8run 8.10, then run from the repository root:

```sh
go run ./examples/advanced/order-worker
```

The local defaults are `http://localhost:8080` with `demo` / `demo`. Override
them with `CAMUNDA_REST_ADDRESS`, `CAMUNDA_BASIC_AUTH_USERNAME`, and
`CAMUNDA_BASIC_AUTH_PASSWORD`.

## Patterns used

**Failure classification.** `OUT_OF_STOCK` returns `*camunda.BpmnError`.
Camunda follows the modeled boundary-error path without consuming a technical
retry. A timeout returns an ordinary Go error, so the SDK fails the job and the
engine decrements retries.

**Bounded worker concurrency.** `WithMaxConcurrentJobs(4)` is a bulkhead for the
inventory service. Choose this from downstream capacity, not CPU count.

**Variable projection.** `WithFetchVariables` requests only data used by the
handler. This reduces payload size and prevents unrelated process data from
becoming an accidental worker dependency.

**Explicit lock budget.** The 20-second job timeout must exceed the expected
handler duration plus acknowledgement time. A timeout that is too short causes
another worker to receive work that may still be executing.

**Graceful drain.** Cancelling the worker stops activation and waits for
in-flight handlers. The SDK acknowledges completed work with a separate bounded
context during shutdown.

## Opinionated recommendations

- Define a small, explicit error taxonomy before writing the handler. Do not
  decide “retry or BPMN error?” ad hoc in catch blocks.
- Return BPMN errors only for outcomes the process explicitly models and can
  act on. Authentication failures, timeouts, and malformed responses are not
  business outcomes.
- Make downstream side effects idempotent using the job key or a business key.
  Camunda provides at-least-once job execution; a timeout can occur after the
  downstream system accepted the request.
- Never set worker concurrency higher than the dependency can sustain.
- Fetch only required variables and validate them before calling a dependency.
- Include the job key, process instance key, attempt, and business key in
  production logs and traces.
- Move retry delays to job retry backoff for real transient incidents; do not
  sleep inside handlers while holding worker capacity.

The in-memory attempt map is a deterministic simulation aid, not a production
retry ledger.
