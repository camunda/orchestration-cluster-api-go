# Advanced runnable examples

The workflow examples deploy their own BPMN model and exit after verifying the
workflow completed. They target a local `c8run` cluster at
`http://localhost:8080` and use `demo` / `demo` by default. Override `CAMUNDA_REST_ADDRESS`,
`CAMUNDA_BASIC_AUTH_USERNAME`, and `CAMUNDA_BASIC_AUTH_PASSWORD` when needed.

Run the commands from the repository root. Keep the leading `./`; without it,
Go interprets `examples/...` as an import path instead of a relative package:

```sh
# Destructive stress test: use only against a disposable local cluster.
go run ./examples/advanced/backpressure

go run ./examples/advanced/order-worker
go run ./examples/advanced/message-correlation
```

- **backpressure** is an intentionally aggressive stress test. Unprotected
  `LEGACY` clients flood the broker with signals while a `BALANCED` client sends
  short-lived, idempotent messages. It succeeds only after observing real
  429/503/`RESOURCE_EXHAUSTED` responses and protected traffic making progress.
  Run it only against a disposable local cluster; tune with `-flooders`,
  `-clients`, and `-duration`.
- **order-worker** treats technical failures as retryable job failures and stock
  shortages as modeled BPMN errors. Do not retry business outcomes as if they
  were infrastructure faults.
- **message-correlation** publishes messages with a useful TTL and a stable
  message ID. This avoids the subscription-open race and makes at-least-once
  producer redelivery safe during the broker's deduplication window.

The embedded credentials are local-development defaults only. Production
applications should use OAuth credentials sourced from a secret store.
