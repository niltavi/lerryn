package blob

import (
	"net/http"
	"testing"
	"time"
)

// The GET Object example from the AWS Signature Version 4 documentation
// for S3 ("Example: GET Object"), which signs exactly the headers this
// store sends: host, range, x-amz-content-sha256 and x-amz-date.
func TestS3SignatureMatchesAWSExample(t *testing.T) {
	s, err := NewS3(S3Options{ // #nosec G101 -- the published AWS example key
		Endpoint:        "https://s3.amazonaws.com",
		Region:          "us-east-1",
		Bucket:          "examplebucket",
		AccessKeyID:     "AKIAIOSFODNN7EXAMPLE",
		SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
	})
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC) }
	req, err := http.NewRequest(http.MethodGet, "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", "bytes=0-9")
	s.sign(req, emptySHA256)
	want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, " +
		"SignedHeaders=host;range;x-amz-content-sha256;x-amz-date, " +
		"Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("Authorization =\n%s\nwant\n%s", got, want)
	}
}

func TestS3Options(t *testing.T) {
	base := S3Options{Endpoint: "http://minio:9000", Bucket: "b", AccessKeyID: "k", SecretAccessKey: "s"}
	for name, mutate := range map[string]func(*S3Options){
		"no scheme":   func(o *S3Options) { o.Endpoint = "minio:9000" },
		"with a path": func(o *S3Options) { o.Endpoint = "http://minio:9000/bucket" },
		"no bucket":   func(o *S3Options) { o.Bucket = "" },
		"no key":      func(o *S3Options) { o.SecretAccessKey = "" },
	} {
		o := base
		mutate(&o)
		if _, err := NewS3(o); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	s, err := NewS3(S3Options{Endpoint: "http://minio:9000", Bucket: "b", Prefix: "team", AccessKeyID: "k", SecretAccessKey: "s"})
	if err != nil {
		t.Fatal(err)
	}
	h := Sum([]byte("x"))
	if got := s.key(h); got != "team/blobs/sha256/"+h[:2]+"/"+h {
		t.Fatalf("key = %s", got)
	}
	if s.opts.Region != "us-east-1" {
		t.Fatalf("default region = %q", s.opts.Region)
	}
}

func TestURIEncode(t *testing.T) {
	if got := uriEncode("/a b/c~d+e=f"); got != "/a%20b/c~d%2Be%3Df" {
		t.Fatalf("uriEncode = %s", got)
	}
}
