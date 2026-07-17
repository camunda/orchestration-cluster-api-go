# Reliable payment correlation

This example deploys `payment.bpmn`, starts three order processes, and publishes
`payment-received` events immediately. Each event is then deliberately delivered
again with the same message ID.

The goal is to demonstrate safe message delivery when the producer and process
subscription become ready at different times and the producer is at-least-once.

## Run it

Start c8run 8.10, then run from the repository root:

```sh
go run ./examples/advanced/message-correlation
```

The local defaults are `http://localhost:8080` with `demo` / `demo`. Override
them with `CAMUNDA_REST_ADDRESS`, `CAMUNDA_BASIC_AUTH_USERNAME`, and
`CAMUNDA_BASIC_AUTH_PASSWORD`.

## Patterns used

**Correlation by business key.** The BPMN subscription evaluates `orderId`;
the publisher uses the same value as the message correlation key. Correlation
keys should be stable domain identifiers, not Camunda-generated technical keys.

**Message buffering.** A ten-minute TTL lets Camunda hold an early payment event
until the process opens its subscription. This removes the race between process
start and event publication.

**Idempotent publication.** The payment provider event ID becomes the Camunda
message ID. A duplicate publication receives HTTP 409 while the original
message is alive; that specific conflict means “already accepted.”

**Eventual-consistency verification.** The example waits for the completed
process through the SDK polling helper because read-side state may lag command
acceptance.

## Opinionated recommendations

- Persist and reuse the source event ID across retries. Never generate a new
  message ID for each attempt.
- Treat only the duplicate-ID 409 as idempotent success. Other 4xx responses are
  defects or bad input and must surface.
- Set TTL to the maximum credible delay between event arrival and subscription
  creation, plus the producer's redelivery window. Longer is not automatically
  safer; buffered messages consume broker resources.
- Use one correlation-key definition across BPMN, producers, documentation, and
  tests. A renamed variable can otherwise turn valid events into silent misses.
- Put only workflow-relevant data in message variables. Store large payment
  payloads externally and pass references.
- Assume publication is at-least-once and consumption may be retried. Every
  downstream side effect must remain idempotent.
- Monitor expired uncorrelated messages. They usually indicate broken
  correlation, ordering assumptions, or an unhealthy process-start path.
