# SDK test drive

This example is the shortest live-cluster confidence check for the complete
hand-written SDK path. It verifies connectivity and FEEL evaluation, deploys a
process, starts an instance, handles and completes a REST job, then polls the
eventually consistent read API until the process is complete.

Start a local Camunda cluster, then run from the repository root:

```sh
go run ./examples/advanced/sdk-test-drive
```

It defaults to `http://localhost:8080` with `demo` / `demo`. Override
`CAMUNDA_REST_ADDRESS`, `CAMUNDA_BASIC_AUTH_USERNAME`, and
`CAMUNDA_BASIC_AUTH_PASSWORD` for another cluster.
