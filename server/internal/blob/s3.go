package blob

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// S3Options configures an S3-compatible store.
type S3Options struct {
	// Endpoint is the service URL, such as https://s3.example.com or
	// http://minio:9000.
	Endpoint string
	// Region is the signing region; S3-compatible services that ignore
	// regions accept us-east-1.
	Region string
	Bucket string
	// Prefix is prepended to every key, so several deployments can share
	// a bucket. It ends with "/" when not empty.
	Prefix string
	// PathStyle addresses the bucket in the path (endpoint/bucket/key)
	// instead of the host name (bucket.endpoint/key). Most self-hosted
	// services need path style.
	PathStyle       bool
	AccessKeyID     string
	SecretAccessKey string
	// HTTPClient sends the requests; nil means a client with the default
	// transport.
	HTTPClient *http.Client
}

// S3 stores objects in an S3-compatible bucket under
// <prefix>blobs/sha256/<first two hex digits>/<hash>, the same layout as
// Local (tech design 6.4). Requests are signed with AWS Signature
// Version 4; uploads carry the content's SHA-256, which is also the object
// name, so the service verifies what it stores.
type S3 struct {
	endpoint *url.URL
	opts     S3Options
	http     *http.Client
	now      func() time.Time
}

var (
	_ Deleter     = (*S3)(nil)
	_ RangeReader = (*S3)(nil)
)

// spoolMemory is how much of an upload is buffered in memory before it
// spills to a temporary file. The content must be hashed before it can
// be named, so it is read completely before the upload starts.
const spoolMemory = 8 << 20

// emptySHA256 is the SHA-256 of an empty payload.
const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// NewS3 returns an S3 store. It does not contact the service; call Check
// to verify the bucket is reachable.
func NewS3(opts S3Options) (*S3, error) {
	u, err := url.Parse(opts.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("blob: s3 endpoint must be an absolute http or https URL, got %q", opts.Endpoint)
	}
	if u.Path != "" && u.Path != "/" {
		return nil, fmt.Errorf("blob: s3 endpoint %q must not have a path", opts.Endpoint)
	}
	u.Path = ""
	if opts.Bucket == "" {
		return nil, errors.New("blob: s3 bucket is required")
	}
	if opts.AccessKeyID == "" || opts.SecretAccessKey == "" {
		return nil, errors.New("blob: s3 access key ID and secret access key are required")
	}
	if opts.Region == "" {
		opts.Region = "us-east-1"
	}
	if opts.Prefix != "" && !strings.HasSuffix(opts.Prefix, "/") {
		opts.Prefix += "/"
	}
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	return &S3{endpoint: u, opts: opts, http: hc, now: time.Now}, nil
}

func (s *S3) key(hash string) string {
	return s.opts.Prefix + "blobs/sha256/" + hash[:2] + "/" + hash
}

// Check verifies that the bucket exists and the credentials may use it.
func (s *S3) Check(ctx context.Context) error {
	resp, err := s.do(ctx, http.MethodHead, "", nil, emptySHA256, 0, nil)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return s.statusError("check bucket", resp)
	}
	return nil
}

// CreateBucket creates the bucket. The server never calls it: buckets,
// their retention and their access policies belong to the operator. It
// exists for setup tools and tests.
func (s *S3) CreateBucket(ctx context.Context) error {
	resp, err := s.do(ctx, http.MethodPut, "", nil, emptySHA256, 0, nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return s.statusError("create bucket", resp)
	}
	return nil
}

// Put implements Store.
func (s *S3) Put(ctx context.Context, r io.Reader) (string, error) {
	sp, err := spool(r)
	if err != nil {
		return "", err
	}
	defer sp.close()
	if ok, err := s.Has(ctx, sp.hash); err != nil {
		return "", err
	} else if ok {
		return sp.hash, nil
	}
	body, err := sp.reader()
	if err != nil {
		return "", err
	}
	resp, err := s.do(ctx, http.MethodPut, s.key(sp.hash), body, sp.hash, sp.size, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", s.statusError("put "+sp.hash, resp)
	}
	return sp.hash, nil
}

// Get implements Store.
func (s *S3) Get(ctx context.Context, hash string) (io.ReadCloser, error) {
	if !ValidHash(hash) {
		return nil, ErrInvalidHash
	}
	resp, err := s.do(ctx, http.MethodGet, s.key(hash), nil, emptySHA256, 0, nil)
	if err != nil {
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return resp.Body, nil
	case http.StatusNotFound:
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%w: %s", ErrNotFound, hash)
	default:
		defer func() { _ = resp.Body.Close() }()
		return nil, s.statusError("get "+hash, resp)
	}
}

// GetRange implements RangeReader.
func (s *S3) GetRange(ctx context.Context, hash string, offset, length int64) (io.ReadCloser, error) {
	if !ValidHash(hash) {
		return nil, ErrInvalidHash
	}
	if offset < 0 || length < 0 {
		return nil, fmt.Errorf("blob: invalid range %d+%d", offset, length)
	}
	if length == 0 {
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	header := http.Header{"Range": {fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)}}
	resp, err := s.do(ctx, http.MethodGet, s.key(hash), nil, emptySHA256, 0, header)
	if err != nil {
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusPartialContent:
		return resp.Body, nil
	case http.StatusNotFound:
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%w: %s", ErrNotFound, hash)
	default:
		defer func() { _ = resp.Body.Close() }()
		return nil, s.statusError("get range of "+hash, resp)
	}
}

// Has implements Store.
func (s *S3) Has(ctx context.Context, hash string) (bool, error) {
	if !ValidHash(hash) {
		return false, ErrInvalidHash
	}
	resp, err := s.do(ctx, http.MethodHead, s.key(hash), nil, emptySHA256, 0, nil)
	if err != nil {
		return false, err
	}
	_ = resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, s.statusError("head "+hash, resp)
	}
}

// Delete implements Deleter. S3 answers 204 whether or not the object
// existed.
func (s *S3) Delete(ctx context.Context, hash string) error {
	if !ValidHash(hash) {
		return ErrInvalidHash
	}
	resp, err := s.do(ctx, http.MethodDelete, s.key(hash), nil, emptySHA256, 0, nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK, http.StatusNotFound:
		return nil
	default:
		return s.statusError("delete "+hash, resp)
	}
}

// statusError describes an unexpected answer. S3 error bodies carry a
// code and a message, never the request's credentials.
func (s *S3) statusError(what string, resp *http.Response) error {
	var body struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	_ = xml.Unmarshal(data, &body)
	if body.Code == "" {
		body.Code = http.StatusText(resp.StatusCode)
	}
	return fmt.Errorf("blob: s3 %s: %d %s: %s", what, resp.StatusCode, body.Code, body.Message)
}

// do sends a signed request for key in the bucket; an empty key addresses
// the bucket itself.
func (s *S3) do(ctx context.Context, method, key string, body io.Reader, payloadHash string, size int64, header http.Header) (*http.Response, error) {
	u := *s.endpoint
	if s.opts.PathStyle {
		u.Path = "/" + s.opts.Bucket + "/" + key
	} else {
		u.Host = s.opts.Bucket + "." + u.Host
		u.Path = "/" + key
	}
	if key == "" {
		u.Path = strings.TrimSuffix(u.Path, "/")
		if u.Path == "" {
			u.Path = "/"
		}
	}
	u.RawPath = uriEncode(u.Path)
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("blob: s3 request: %w", err)
	}
	for k, vs := range header {
		req.Header[k] = vs
	}
	if body != nil {
		req.ContentLength = size
	}
	s.sign(req, payloadHash)
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("blob: s3 %s: %w", method, err)
	}
	return resp, nil
}

// sign adds AWS Signature Version 4 headers to req.
func (s *S3) sign(req *http.Request, payloadHash string) {
	now := s.now().UTC()
	amzDate := now.Format("20060102T150405Z")
	day := now.Format("20060102")
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)

	signed := map[string]string{"host": req.URL.Host}
	for k, vs := range req.Header {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "x-amz-") || lk == "range" {
			signed[lk] = strings.TrimSpace(strings.Join(vs, ","))
		}
	}
	names := make([]string, 0, len(signed))
	for k := range signed {
		names = append(names, k)
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, k := range names {
		canonHeaders.WriteString(k + ":" + signed[k] + "\n")
	}
	signedHeaders := strings.Join(names, ";")

	canonical := strings.Join([]string{
		req.Method,
		req.URL.EscapedPath(),
		canonicalQuery(req.URL.Query()),
		canonHeaders.String(),
		signedHeaders,
		payloadHash,
	}, "\n")
	scope := day + "/" + s.opts.Region + "/s3/aws4_request"
	sum := sha256.Sum256([]byte(canonical))
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(sum[:])

	k := hmacSHA256([]byte("AWS4"+s.opts.SecretAccessKey), day)
	k = hmacSHA256(k, s.opts.Region)
	k = hmacSHA256(k, "s3")
	k = hmacSHA256(k, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(k, stringToSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+s.opts.AccessKeyID+"/"+scope+
		", SignedHeaders="+signedHeaders+", Signature="+signature)
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func canonicalQuery(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, uriEncode(k)+"="+uriEncode(v))
		}
	}
	return strings.Join(parts, "&")
}

// uriEncode escapes s as SigV4 requires: everything except unreserved
// characters and '/' is percent-encoded with upper-case hex digits.
func uriEncode(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~', c == '/':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// spooled is upload content read completely and hashed.
type spooled struct {
	hash string
	size int64
	mem  []byte
	file *os.File
}

func spool(r io.Reader) (*spooled, error) {
	h := sha256.New()
	var buf bytes.Buffer
	n, err := io.Copy(io.MultiWriter(&buf, h), io.LimitReader(r, spoolMemory+1))
	if err != nil {
		return nil, fmt.Errorf("blob: read upload: %w", err)
	}
	sp := &spooled{size: n}
	if n <= spoolMemory {
		sp.mem = buf.Bytes()
		sp.hash = hex.EncodeToString(h.Sum(nil))
		return sp, nil
	}
	f, err := os.CreateTemp("", "lerryn-s3-upload-*")
	if err != nil {
		return nil, fmt.Errorf("blob: spool upload: %w", err)
	}
	sp.file = f
	_ = os.Remove(f.Name()) // unlinked at once; the open handle keeps the data
	if _, err := f.Write(buf.Bytes()); err != nil {
		sp.close()
		return nil, fmt.Errorf("blob: spool upload: %w", err)
	}
	rest, err := io.Copy(io.MultiWriter(f, h), r)
	if err != nil {
		sp.close()
		return nil, fmt.Errorf("blob: spool upload: %w", err)
	}
	sp.size += rest
	sp.hash = hex.EncodeToString(h.Sum(nil))
	return sp, nil
}

func (sp *spooled) reader() (io.Reader, error) {
	if sp.file == nil {
		return bytes.NewReader(sp.mem), nil
	}
	if _, err := sp.file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("blob: rewind upload: %w", err)
	}
	return sp.file, nil
}

func (sp *spooled) close() {
	if sp.file != nil {
		_ = sp.file.Close()
	}
}
