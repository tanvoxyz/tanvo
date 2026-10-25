package blob

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/dockertest"
)

const (
	minioImage  = "minio/minio:RELEASE.2025-09-07T16-13-09Z"
	minioUser   = "tanvotest"
	minioSecret = "tanvotestsecret"
)

// startMinIO runs MinIO in a container on the local Docker engine and
// returns its endpoint; it skips the test when no Docker engine is
// reachable.
func startMinIO(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping MinIO integration test in short mode")
	}
	ctx := t.Context()
	engine, err := dockertest.Connect(ctx)
	if errors.Is(err, dockertest.ErrUnavailable) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	c, err := engine.Run(ctx, dockertest.Spec{
		Image: minioImage,
		Env:   []string{"MINIO_ROOT_USER=" + minioUser, "MINIO_ROOT_PASSWORD=" + minioSecret},
		Cmd:   []string{"server", "/data"},
		Ports: []string{"9000/tcp"},
	})
	if err != nil {
		t.Skipf("start MinIO: %v", err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	endpoint := "http://" + c.Addr("9000/tcp")
	if err := dockertest.Wait(ctx, time.Minute, func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/minio/health/live", nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		drain(resp)
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("health got = %d, want = 200", resp.StatusCode)
		}
		return nil
	}); err != nil {
		t.Fatalf("MinIO not ready: %v", err)
	}
	return endpoint
}

func TestS3MinIO(t *testing.T) {
	endpoint := startMinIO(t)
	ctx := context.Background()
	s, err := NewS3(S3Options{
		Endpoint: endpoint, Region: "us-east-1", Bucket: "tanvo",
		AccessKeyID: minioUser, SecretAccessKey: minioSecret,
		PathStyle: true, Prefix: "documents", URLExpiry: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	// CreateBucket: a PUT of the bucket itself.
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint+"/tanvo", nil)
	if err != nil {
		t.Fatal(err)
	}
	s.signer.sign(req, hexSHA256(nil), time.Now())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	drain(resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CreateBucket = %d, want = 200", resp.StatusCode)
	}

	a, b := testOrgs(t)
	body := bytes.Repeat([]byte("tanvo "), 100_000)
	ref, err := s.Put(ctx, a, "Facture n°1.pdf", "application/pdf", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Put = %v, want = nil", err)
	}
	rc, err := s.Get(ctx, a, ref)
	if err != nil {
		t.Fatalf("Get = %v, want = nil", err)
	}
	if got := readAll(t, rc); got != string(body) {
		t.Errorf("body length = %d, want = %d", len(got), len(body))
	}
	if _, err := s.Get(ctx, b, ref); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Get from other org = %v, want = ErrNotFound", err)
	}
	u, err := s.URL(ctx, a, ref)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	ct := resp.Header.Get("Content-Type")
	drain(resp)
	if resp.StatusCode != http.StatusOK || ct != "application/pdf" {
		t.Errorf("GET presigned URL = %d %s, want = 200 application/pdf", resp.StatusCode, ct)
	}
	if err := s.Delete(ctx, a, ref); err != nil {
		t.Fatalf("Delete = %v, want = nil", err)
	}
	if _, err := s.Get(ctx, a, ref); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Get after Delete = %v, want = ErrNotFound", err)
	}
	if err := s.Delete(ctx, a, ref); err != nil {
		t.Errorf("second Delete = %v, want = nil", err)
	}
}
