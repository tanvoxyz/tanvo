package ingest

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/ingest/internal/dockertest"
)

// connectImage is the Redpanda Connect image of connectors/Dockerfile.
const connectImage = "docker.io/redpandadata/connect:4.83.0"

// TestConnectorsRedpandaConnect lints the Redpanda Connect configurations
// in connectors/ and runs their mapping tests in the Redpanda Connect image
// of connectors/Dockerfile. The linter runs with the required environment
// of README.md only, so a variable the README marks optional
// (SQS_DLQ_ENDPOINT, KAFKA_TOPIC, the batching) must have a default.
func TestConnectorsRedpandaConnect(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	engine, err := dockertest.Connect(ctx)
	if errors.Is(err, dockertest.ErrUnavailable) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.Abs("connectors")
	if err != nil {
		t.Fatal(err)
	}
	files := []string{"/connectors/http.yml", "/connectors/sqs.yml", "/connectors/kinesis.yml"}
	required := []string{
		"KAFKA_BROKERS=127.0.0.1:9092", "KAFKA_USER=tanvo", "KAFKA_PASSWORD=tanvo",
		"ORGANIZATION_ID=0199a000-0000-7000-8000-0000000000aa",
		"SQS_ENDPOINT=https://sqs.eu-west-1.amazonaws.com/000000000000/events", "SQS_REGION=eu-west-1",
		"SQS_KEY_ID=key", "SQS_KEY_SECRET=secret",
		"KINESIS_STREAM=events", "AWS_REGION=eu-west-1", "AWS_ROLE=arn:aws:iam::000000000000:role/tanvo",
		"AWS_ROLE_EXTERNAL_ID=tanvo", "DYNAMODB_TABLE=tanvo-checkpoints",
	}
	for _, tt := range []struct {
		name string
		env  []string
		cmd  []string
	}{
		{name: "lint", env: required, cmd: append([]string{"lint", "--deprecated"}, files...)},
		{name: "test", cmd: append([]string{"test"}, files...)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, err := engine.Run(ctx, dockertest.Spec{
				Image: connectImage, Env: tt.env, Cmd: tt.cmd,
				Binds: []string{dir + ":/connectors:ro"}, Keep: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = c.Close(context.WithoutCancel(ctx)) }()
			code, err := c.Exit(ctx)
			if err != nil {
				t.Fatal(err)
			}
			out, err := c.Logs(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if code != 0 {
				t.Errorf("redpanda-connect %v exit code got = %d, want = 0; output:\n%s", tt.cmd, code, out)
			}
		})
	}
}
