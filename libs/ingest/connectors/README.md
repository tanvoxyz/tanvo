# Ingestion connectors

[Redpanda Connect](https://docs.redpanda.com/redpanda-connect/) configurations that read usage events from an HTTP endpoint, an SQS queue or a Kinesis stream and produce them to the raw events topic of Tanvo's Kafka ingestion (`events.raw`, [architecture §7](../../../docs/architecture.md)). The server's intake consumer validates and enriches them as it does the events of the API; an invalid one goes to the dead letter topic. The organization must ingest through Kafka.

Build the image with `docker build -t tanvo-connect libs/ingest/connectors` and run one configuration with `docker run --env-file … tanvo-connect run /http.yml` (or `sqs.yml`, `kinesis.yml`). `rpk connect test sqs.yml` runs the mapping tests.

## Events

HTTP and SQS take the event under `event`; Kinesis takes it bare. `timestamp` is Unix seconds (a fraction is allowed) and defaults to the ingestion time; `precise_total_amount_cents` defaults to 0.

```json
{
  "event": {
    "external_subscription_id": "sub_1",
    "transaction_id": "unique_transaction_id",
    "code": "billable_metric_code",
    "timestamp": 1620000000,
    "properties": {"my_property": "my_value"},
    "precise_total_amount_cents": 1000
  }
}
```

HTTP events carry their `organization_id` under `event`; SQS and Kinesis take it from `ORGANIZATION_ID`. Records are keyed `organization/external subscription/transaction`, as the API keys them.

## Environment

| Variable | Connector | Description | Required |
| :--- | :--- | :--- | :--- |
| `KAFKA_BROKERS` | all | Kafka seed brokers | yes |
| `KAFKA_USER`, `KAFKA_PASSWORD` | all | SCRAM-SHA-512 credentials | yes |
| `KAFKA_TOPIC` | all | Raw events topic, default `events.raw` (add the server's topic prefix) | no |
| `KAFKA_TLS` | HTTP, Kinesis | TLS, default `false`; SQS always uses TLS | no |
| `KAFKA_BATCH_COUNT`, `KAFKA_BATCH_BYTE_SIZE`, `KAFKA_BATCH_PERIOD` | HTTP, Kinesis | Batching, default 100 messages, 1 MB, 1s | no |
| `ORGANIZATION_ID` | SQS, Kinesis | Organization of the events | yes |
| `SQS_ENDPOINT`, `SQS_REGION`, `SQS_KEY_ID`, `SQS_KEY_SECRET` | SQS | Queue and AWS credentials | yes |
| `SQS_DLQ_ENDPOINT` | SQS | Queue receiving the events that fail to map | no |
| `KINESIS_STREAM`, `AWS_REGION` | Kinesis | Stream and region | yes |
| `AWS_ROLE`, `AWS_ROLE_EXTERNAL_ID` | Kinesis | IAM role assumed and its external ID | yes |
| `DYNAMODB_TABLE` | Kinesis | Checkpoint table | yes |

The HTTP connector listens on port 3000 and takes `POST /events`. Every connector serves Prometheus metrics.
