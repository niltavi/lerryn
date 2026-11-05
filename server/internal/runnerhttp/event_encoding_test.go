package runnerhttp

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/runproto"
)

func TestClientPreservesResultJSON(t *testing.T) {
	for _, tt := range []struct{ name, raw string }{
		{name: "HTML", raw: `{"html":"<p>hello</p>&"}`},
		{name: "Unicode separators", raw: `{"separators":"` + "\u2028\u2029" + `"}`},
		{name: "existing escapes", raw: `{"escaped":"\u003c\u003E\u0026\u2028\u2029\uD83D\uDE00"}`},
		{name: "literal escape and large number", raw: `{"literal":"\\u003c\\u2028\\u2029","number":9007199254740993}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewClient(ClientOptions{
				ServerURL: "https://lerryn.example.com", RegistrationToken: "registration", CredentialFile: filepath.Join(t.TempDir(), "credential"),
				HTTPClient: &http.Client{Transport: budgetRoundTrip(func(req *http.Request) (*http.Response, error) {
					var body eventsRequest
					if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if len(body.Events) != 1 || body.Events[0].Result == nil || !bytes.Equal(body.Events[0].Result.Data, []byte(tt.raw)) {
						t.Fatalf("raw result JSON changed on the wire: %+v; want %q", body.Events, tt.raw)
					}
					return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
				})},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := client.AppendEvents(t.Context(), "remote", uuid.New(), []runproto.Event{{
				Seq: 1, Kind: runproto.EventResult, Result: &runproto.Result{Status: "ok", Data: json.RawMessage(tt.raw)},
			}}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type spaceReader struct{}

func (spaceReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = ' '
	}
	return len(p), nil
}

// Use insignificant whitespace so the protocol limit is exercised without
// allocating a sixteen-MiB JSON value or weakening a domain's own limit.
func TestProtocolRequestByteLimit(t *testing.T) {
	const limit = 16 << 20
	if maxRequestBytes != limit {
		t.Fatalf("request limit changed to %d bytes", maxRequestBytes)
	}
	h := &handler{log: slog.New(slog.DiscardHandler)}
	next := h.protocol(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if h.decode(w, r, &body) {
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	for _, extra := range []int64{0, 1} {
		t.Run(strconv.FormatInt(extra, 10), func(t *testing.T) {
			body := io.MultiReader(strings.NewReader(`{}`), io.LimitReader(spaceReader{}, limit-2+extra))
			req := httptest.NewRequest(http.MethodPost, pathClaim, body)
			req.Header.Set(ProtocolHeader, strconv.Itoa(clientProtocol))
			rec := httptest.NewRecorder()
			next.ServeHTTP(rec, req)
			if extra == 0 && rec.Code != http.StatusNoContent {
				t.Fatalf("request at the byte limit = %d, %s", rec.Code, rec.Body.String())
			}
			if extra > 0 && (rec.Code < 400 || !strings.Contains(rec.Body.String(), codeRejected)) {
				t.Fatalf("oversized request was accepted: %d, %s", rec.Code, rec.Body.String())
			}
		})
	}
}
