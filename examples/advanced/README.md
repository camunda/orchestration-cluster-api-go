# Advanced runnable examples

These examples deploy their own BPMN model and exit after verifying the workflow
completed. They target a local `c8run` cluster at `http://localhost:8080` and use
`demo` / `demo` by default. Override `CAMUNDA_REST_ADDRESS`,
`CAMUNDA_BASIC_AUTH_USERNAME`, and `CAMUNDA_BASIC_AUTH_PASSWORD` when needed.

```sh
go run ./examples/advanced/backpressure
go run ./examples/advanced/order-worker
go run ./examples/advanced/message-correlation
```

- **backpressure** combines the SDK's `BALANCED` adaptive limiter and transient
  retries with bounded producer fan-out and bounded worker concurrency. Keep both:
  the SDK responds to cluster signals, while the application bound prevents a
  large local backlog before the first signal arrives.
- **order-worker** treats technical failures as retryable job failures and stock
  shortages as modeled BPMN errors. Do not retry business outcomes as if they
  were infrastructure faults.
- **message-correlation** publishes messages with a useful TTL and a stable
  message ID. This avoids the subscription-open race and makes at-least-once
  producer redelivery safe during the broker's deduplication window.

The embedded credentials are local-development defaults only. Production
applications should use OAuth credentials sourced from a secret store.
