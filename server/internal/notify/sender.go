package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"
)

// Timeout is how long a receiver has to answer (contract,
// webhooks.notification).
const Timeout = 10 * time.Second

// maxResponseBytes caps how much of a response is read to judge it.
const maxResponseBytes = 64 << 10

var errInvalidConfirmation = errors.New("notify: invalid bot confirmation")

// Sender posts notifications through a guarded HTTP client: connections
// to internal addresses fail unless allowed, redirects are not followed
// (a redirect could lead anywhere) and proxies from the environment are
// ignored, since a proxy would hide the real target from the guard.
type Sender struct {
	client *http.Client
}

// NewSender returns a sender whose connections g checks. A zero timeout
// means Timeout.
func NewSender(g *Guard, timeout time.Duration) *Sender {
	if timeout <= 0 {
		timeout = Timeout
	}
	return &Sender{client: g.HTTPClient(timeout)}
}

// HTTPClient enforces the address policy at dial time, without redirects or proxies.
func (g *Guard) HTTPClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: timeout, Control: g.control}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// StatusError is a response that does not count as delivered.
type StatusError struct {
	Status int
	// Platform is the error a bot platform reported in a 2xx body, such
	// as Feishu's "code" and "msg"; empty otherwise.
	Platform string
}

func (e *StatusError) Error() string {
	if e.Platform != "" {
		return fmt.Sprintf("notify: receiver answered %d with error %s", e.Status, e.Platform)
	}
	return fmt.Sprintf("notify: receiver answered %d", e.Status)
}

// Send posts req to target for a channel of kind. It returns the response
// status, 0 when no response arrived, and an error unless the
// notification counts as delivered: a 2xx answer, with a complete success
// confirmation for Feishu, which reports errors with status 200.
func (s *Sender) Send(ctx context.Context, kind, target string, req Request) (int, error) {
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(req.Body))
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrInvalidURL, err)
	}
	if r.URL.Scheme != "http" && r.URL.Scheme != "https" {
		return 0, ErrInvalidURL
	}
	r.Header = req.Header.Clone()
	resp, err := s.client.Do(r)
	if err != nil {
		// *url.Error quotes the URL, which may hold a bot token; the
		// error ends up in logs, so only the cause is returned.
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err
		}
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, &StatusError{Status: resp.StatusCode}
	}
	if kind == KindFeishu {
		// A successful HTTP status alone cannot confirm a bot delivery:
		// truncated JSON may hide a nonzero platform code.
		if readErr != nil {
			return resp.StatusCode, fmt.Errorf("%w: read response: %w", errInvalidConfirmation, readErr)
		}
		if len(body) > maxResponseBytes {
			return resp.StatusCode, errInvalidConfirmation
		}
		msg, err := feishuError(body)
		if err != nil {
			return resp.StatusCode, err
		}
		if msg != "" {
			return resp.StatusCode, &StatusError{Status: resp.StatusCode, Platform: msg}
		}
	}
	return resp.StatusCode, nil
}

// feishuError returns the platform's error message or rejects a response
// that cannot confirm delivery. Current responses carry code and msg,
// older ones StatusCode and StatusMessage; both use 0 for success.
func feishuError(body []byte) (string, error) {
	var r struct {
		Code          *int   `json:"code"`
		Msg           string `json:"msg"`
		StatusCode    *int   `json:"StatusCode"`
		StatusMessage string `json:"StatusMessage"`
	}
	if err := json.Unmarshal(body, &r); err != nil || r.Code == nil && r.StatusCode == nil {
		return "", errInvalidConfirmation
	}
	switch {
	case r.Code != nil && *r.Code != 0:
		return fmt.Sprintf("%d %s", *r.Code, r.Msg), nil
	case r.Code == nil && r.StatusCode != nil && *r.StatusCode != 0:
		return fmt.Sprintf("%d %s", *r.StatusCode, r.StatusMessage), nil
	}
	return "", nil
}

// Describe explains a Send error in the words stored on the delivery and
// shown in the console. It never includes the target URL, which may hold
// a bot token.
func Describe(err error) string {
	var (
		status *StatusError
		dns    *net.DNSError
		netErr net.Error
		cert   *tls.CertificateVerificationError
		unk    x509.UnknownAuthorityError
		host   x509.HostnameError
	)
	switch {
	case err == nil:
		return ""
	case errors.As(err, &status):
		if status.Platform != "" {
			return truncate("对方返回错误："+status.Platform, MaxReasonRunes)
		}
		if status.Status >= 300 && status.Status < 400 {
			return fmt.Sprintf("对方返回 HTTP %d 重定向，通知不跟随重定向", status.Status)
		}
		return fmt.Sprintf("对方返回 HTTP %d", status.Status)
	case errors.Is(err, ErrBlockedAddress):
		return "目标地址指向内网或本机，部署没有允许这类地址"
	case errors.Is(err, ErrInvalidURL):
		return "目标地址无效"
	case errors.As(err, &dns):
		return "无法解析目标地址的域名"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return fmt.Sprintf("%d 秒内没有响应", int(Timeout/time.Second))
	case errors.Is(err, syscall.ECONNREFUSED):
		return "连接被拒绝"
	case errors.As(err, &cert), errors.As(err, &unk), errors.As(err, &host):
		return "TLS 证书校验失败"
	case errors.Is(err, context.Canceled):
		return "发送被中止"
	case errors.Is(err, errInvalidConfirmation):
		return "无法确认对方是否接收通知"
	default:
		return "连接失败"
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
