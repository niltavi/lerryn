//go:build integration

package blob_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/niltavi/lerryn/server/internal/blob"
)

// The S3 store against a MinIO container:
//
//	go test -tags integration -run TestS3 ./server/internal/blob/
//
// LERRYN_TEST_S3_IMAGE overrides the image. MinIO no longer publishes
// images on Docker Hub; the default is a community build of the same
// server.
const defaultMinIOImage = "pgsty/minio:latest"

const (
	minioUser     = "lerryn-test"
	minioPassword = "lerryn-test-secret" // #nosec G101 -- a throwaway test container
)

// startMinIO runs MinIO on a free local port, creates a bucket and
// returns the endpoint. The container is removed when the test ends.
func startMinIO(t *testing.T) string {
	t.Helper()
	image := os.Getenv("LERRYN_TEST_S3_IMAGE")
	if image == "" {
		image = defaultMinIOImage
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	out, err := exec.Command("docker", "run", "-d", "--rm",
		"-e", "MINIO_ROOT_USER="+minioUser, "-e", "MINIO_ROOT_PASSWORD="+minioPassword,
		"-p", "127.0.0.1:"+strconv.Itoa(port)+":9000", image, "server", "/data").CombinedOutput() // #nosec G204 G702 -- test-controlled arguments
	if err != nil {
		t.Fatalf("start MinIO (%s): %v: %s", image, err, out)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() }) // #nosec G204 G702 -- the container this test started
	endpoint := "http://127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(60 * time.Second)
	for {
		resp, err := http.Get(endpoint + "/minio/health/ready")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("MinIO did not become ready")
		}
		time.Sleep(200 * time.Millisecond)
	}
	return endpoint
}

func createBucket(t *testing.T, s *blob.S3, endpoint, bucket string) {
	t.Helper()
	if err := s.CreateBucket(context.Background()); err != nil {
		t.Fatalf("create bucket %s at %s: %v", bucket, endpoint, err)
	}
}

func TestS3AgainstMinIO(t *testing.T) {
	endpoint := startMinIO(t)
	ctx := context.Background()
	s, err := blob.NewS3(blob.S3Options{
		Endpoint: endpoint, Bucket: "lerryn", Prefix: "deploy-a", PathStyle: true,
		AccessKeyID: minioUser, SecretAccessKey: minioPassword,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Check(ctx); err == nil {
		t.Fatal("Check passed for a bucket that does not exist")
	}
	createBucket(t, s, endpoint, "lerryn")
	if err := s.Check(ctx); err != nil {
		t.Fatalf("check: %v", err)
	}

	small := []byte("print('hello')\n")
	hash, err := s.Put(ctx, bytes.NewReader(small))
	if err != nil {
		t.Fatal(err)
	}
	if hash != blob.Sum(small) {
		t.Fatalf("hash = %s", hash)
	}
	if again, err := s.Put(ctx, bytes.NewReader(small)); err != nil || again != hash {
		t.Fatalf("second put = %s, %v", again, err)
	}
	if ok, err := s.Has(ctx, hash); err != nil || !ok {
		t.Fatalf("has = %v, %v", ok, err)
	}
	got, err := blob.ReadAll(ctx, s, hash)
	if err != nil || !bytes.Equal(got, small) {
		t.Fatalf("read = %q, %v", got, err)
	}
	part, err := blob.ReadRange(ctx, s, hash, 6, 5)
	if err != nil || string(part) != "'hell" {
		t.Fatalf("range = %q, %v", part, err)
	}
	if empty, err := blob.ReadRange(ctx, s, hash, 3, 0); err != nil || len(empty) != 0 {
		t.Fatalf("empty range = %q, %v", empty, err)
	}

	// Deleting removes the object; deleting it again, or deleting an
	// object never stored, succeeds.
	gone, err := s.Put(ctx, bytes.NewReader([]byte("expired artifact")))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.Delete(ctx, gone); err != nil {
			t.Fatalf("delete: %v", err)
		}
	}
	if ok, err := s.Has(ctx, gone); err != nil || ok {
		t.Fatalf("has after delete = %v, %v", ok, err)
	}
	if ok, err := s.Has(ctx, hash); err != nil || !ok {
		t.Fatalf("delete removed another object: %v, %v", ok, err)
	}
	if err := s.Delete(ctx, "../etc/passwd"); !errors.Is(err, blob.ErrInvalidHash) {
		t.Fatalf("delete with an invalid name: %v", err)
	}

	missing := blob.Sum([]byte("missing"))
	if ok, err := s.Has(ctx, missing); err != nil || ok {
		t.Fatalf("has missing = %v, %v", ok, err)
	}
	if _, err := s.Get(ctx, missing); !errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("get missing: %v", err)
	}
	if _, err := s.Get(ctx, "../etc/passwd"); !errors.Is(err, blob.ErrInvalidHash) {
		t.Fatalf("get with an invalid name: %v", err)
	}

	// Larger than the in-memory spool, so the upload goes through a file.
	large := make([]byte, 9<<20)
	if _, err := rand.Read(large); err != nil {
		t.Fatal(err)
	}
	lh, err := s.Put(ctx, bytes.NewReader(large))
	if err != nil {
		t.Fatal(err)
	}
	rc, err := s.Get(ctx, lh)
	if err != nil {
		t.Fatal(err)
	}
	read, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil || !bytes.Equal(read, large) {
		t.Fatalf("large object read back %d bytes, %v", len(read), err)
	}

	wrong, err := blob.NewS3(blob.S3Options{
		Endpoint: endpoint, Bucket: "lerryn", PathStyle: true,
		AccessKeyID: minioUser, SecretAccessKey: "not-the-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := wrong.Check(ctx); err == nil || strings.Contains(err.Error(), "not-the-secret") {
		t.Fatalf("check with a wrong secret: %v", err)
	}
}
