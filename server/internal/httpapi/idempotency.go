package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"go.yaml.in/yaml/v3"

	"github.com/niltavi/lerryn/api"
	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/service"
)

// Idempotency stores the first response to each idempotency key.
// *service.Idempotency implements it.
type Idempotency interface {
	Begin(ctx context.Context, p *auth.Principal, key, operation, hash string) (service.IdempotencyClaim, error)
	Complete(ctx context.Context, c service.IdempotencyClaim, status int, body string) error
	Release(ctx context.Context, c service.IdempotencyClaim) error
}

// IdempotencyKeyHeader is the request header of idempotency keys; the
// header ReplayedHeader marks a response replayed from an earlier request.
const (
	IdempotencyKeyHeader = "Idempotency-Key"
	ReplayedHeader       = "Idempotent-Replayed"
	// maxStoredResponse bounds what a key remembers. Responses of the
	// operations that take keys are far smaller; a larger one is not
	// stored, and a retry runs again.
	maxStoredResponse = 1 << 20
)

// idempotentOperations are the operations whose contract declares the
// Idempotency-Key header, so adding the header to an operation in api/
// is all it takes.
var idempotentOperations = func() map[string]bool {
	ops, err := operationsWithHeader(api.Spec, IdempotencyKeyHeader)
	if err != nil {
		panic(fmt.Sprintf("httpapi: read idempotent operations from the embedded spec: %v", err))
	}
	return ops
}()

func operationsWithHeader(spec []byte, header string) (map[string]bool, error) {
	type param struct {
		Ref  string `yaml:"$ref"`
		Name string `yaml:"name"`
		In   string `yaml:"in"`
	}
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string  `yaml:"operationId"`
			Parameters  []param `yaml:"parameters"`
		} `yaml:"paths"`
		Components struct {
			Parameters map[string]param `yaml:"parameters"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(spec, &doc); err != nil {
		return nil, err
	}
	const prefix = "#/components/parameters/"
	ops := map[string]bool{}
	for _, item := range doc.Paths {
		for _, op := range item {
			for _, p := range op.Parameters {
				if len(p.Ref) > len(prefix) && p.Ref[:len(prefix)] == prefix {
					p = doc.Components.Parameters[p.Ref[len(prefix):]]
				}
				if p.In == "header" && http.CanonicalHeaderKey(p.Name) == header && op.OperationID != "" {
					ops[op.OperationID] = true
				}
			}
		}
	}
	return ops, nil
}

// idempotent replays the stored response of a request repeated with the
// same Idempotency-Key, and stores the first response (tech design 9.3).
// It runs after authentication and contract validation, so a key is only
// spent on a well-formed request by a known caller.
func (h *handler) idempotent(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get(IdempotencyKeyHeader)
		p := auth.PrincipalFrom(r.Context())
		if key == "" || h.idempotency == nil || p == nil {
			next.ServeHTTP(w, r)
			return
		}
		route, _, err := contractRouter.FindRoute(r)
		if err != nil || route.Operation == nil || !idempotentOperations[route.Operation.OperationID] {
			next.ServeHTTP(w, r)
			return
		}
		// Apply the access rules before touching the key: otherwise a
		// caller lacking the scope could replay a stored response, and a
		// refused request would take the key from a later allowed one.
		// requireAccess answers the refused request as usual.
		rules, known := operations[operationKey(route.Operation.OperationID)]
		if status, _ := denial(p, rules); !known || status != 0 {
			next.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeProblem(w, newProblem(r, http.StatusBadRequest, ErrorCodeValidationFailed, "请求体无法读取"))
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))

		claim, err := h.idempotency.Begin(r.Context(), p, key, route.Operation.OperationID, requestHash(r, body))
		if errors.Is(err, service.ErrIdempotencyConflict) {
			writeProblem(w, newProblem(r, http.StatusConflict, ErrorCodeIdempotencyConflict, "这个幂等键已用于另一个请求，请换一个新的键"))
			return
		}
		if err != nil {
			if prob, ok := lifecycleProblem(r.Context(), err); ok {
				prob.Instance = new(r.URL.Path)
				writeProblem(w, prob)
				return
			}
			h.responseError(w, r, fmt.Errorf("idempotency key: %w", err))
			return
		}
		if rep := claim.Replay; rep != nil {
			contentType := "application/json"
			if rep.Status >= 400 {
				contentType = "application/problem+json"
			}
			w.Header().Set("Content-Type", contentType)
			w.Header().Set(ReplayedHeader, "true")
			w.WriteHeader(rep.Status)
			_, _ = io.WriteString(w, rep.Body)
			return
		}

		rec := &capture{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		// The outcome is stored even if the client went away: that is
		// exactly the case a retry with the same key is for.
		ctx := context.WithoutCancel(r.Context())
		if rec.status >= 500 || rec.overflow {
			// Worth retrying, or too large to replay: the key is free again.
			err = h.idempotency.Release(ctx, claim)
		} else {
			err = h.idempotency.Complete(ctx, claim, rec.status, rec.body.String())
		}
		if err != nil {
			h.log.ErrorContext(ctx, "record idempotent response", "operation", route.Operation.OperationID,
				"request_id", RequestIDFromContext(ctx), "error", err)
		}
	})
}

// requestHash identifies a request: method, path and query, If-Match and
// the body as canonical JSON, so the same request sent with different
// formatting still matches.
func requestHash(r *http.Request, body []byte) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\n%s\n%s\n", r.Method, r.URL.RequestURI(), r.Header.Get("If-Match"))
	if canonical, err := canonicalJSON(body); err == nil {
		body = canonical
	}
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

func canonicalJSON(b []byte) ([]byte, error) {
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, nil
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	if d.More() {
		return nil, errors.New("trailing data")
	}
	// Maps marshal with sorted keys.
	return json.Marshal(v)
}

// capture passes a response through while keeping a copy of it.
type capture struct {
	http.ResponseWriter
	status   int
	wrote    bool
	body     bytes.Buffer
	overflow bool
}

func (c *capture) WriteHeader(status int) {
	if !c.wrote {
		c.status, c.wrote = status, true
	}
	c.ResponseWriter.WriteHeader(status)
}

func (c *capture) Write(p []byte) (int, error) {
	c.wrote = true
	if !c.overflow {
		if c.body.Len()+len(p) > maxStoredResponse {
			c.overflow = true
			c.body.Reset()
		} else {
			c.body.Write(p)
		}
	}
	return c.ResponseWriter.Write(p)
}

func (c *capture) Unwrap() http.ResponseWriter { return c.ResponseWriter }
