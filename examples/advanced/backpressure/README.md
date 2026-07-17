# Surviving cluster backpressure

This is a destructive stress test for a **disposable local cluster**. It models a
Black Friday incident:

- a runaway warehouse integration broadcasts `inventory-level-changed` signals;
- the cluster starts rejecting work with 429, 503, or `RESOURCE_EXHAUSTED`;
- checkout continues publishing idempotent `payment-received` events;
- accepted payments start the embedded `payment-intake.bpmn` process.

The program passes only after it observes real cluster backpressure and checkout
then completes 100 additional payment publications.

## Run it

Start c8run 8.10, then run from the repository root:

```sh
go run ./examples/advanced/backpressure
```

Defaults are deliberately aggressive: 512 pressure generators, 64 protected
clients, and a 30-second deadline. If your machine does not trigger
backpressure:

```sh
go run ./examples/advanced/backpressure \
  -flooders 1024 \
  -clients 128 \
  -duration 60s
```

The local defaults are `http://localhost:8080` with `demo` / `demo`. Override
them with `CAMUNDA_REST_ADDRESS`, `CAMUNDA_BASIC_AUTH_USERNAME`, and
`CAMUNDA_BASIC_AUTH_PASSWORD`.

## Patterns used

**Adaptive concurrency control.** Production traffic uses the `BALANCED`
profile. Once the cluster signals overload, the SDK reduces concurrent requests
instead of allowing every caller to pile on.

**Bulkhead isolation.** Pressure traffic and checkout traffic use separate
clients. In a real system, isolate critical traffic by service and workload;
never let a bulk importer share an unbounded work queue with checkout.

**Retry with full jitter.** The SDK retries transient HTTP failures. If all
transport attempts fail, the application retries the same logical payment with
jitter, avoiding a synchronized retry storm.

**Idempotent producer.** Every payment has a stable provider event ID. Retrying
the same event is safe; HTTP 409 means Camunda already accepted that ID during
its TTL window.

**Proof after the signal.** The test snapshots completed payment traffic when
the first backpressure response arrives. Successes before overload do not count
toward recovery.

## Opinionated recommendations

- Keep `BALANCED` in production. `LEGACY` is used here only to manufacture a
  noisy neighbor and should not be copied into application code.
- Bound concurrency at the application boundary too. Adaptive control reacts
  after a signal; a local bulkhead prevents an unlimited backlog before then.
- Retry only operations with an idempotency strategy. Blindly retrying process
  starts or side effects can duplicate business work.
- Use stable event IDs from the source system, not random IDs generated per
  attempt.
- Set TTL from the producer's real redelivery window. Thirty seconds is short
  here so the stress test cleans itself up quickly.
- Measure final failures, backpressure responses, latency, and queue depth
  separately. A falling error rate with an exploding queue is not recovery.
- Do not run this against a shared development, staging, or production cluster.

## Expected output

```text
  1.0s pressure: ok=4575 bp=10827 err=0 | protected: ok=80 bp=75 err=0
observed 25516 real backpressure responses; protected traffic completed 100 requests afterward
```

`bp` counts genuine cluster responses, not locally fabricated errors.
