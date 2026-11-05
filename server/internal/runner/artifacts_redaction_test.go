package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/runproto"
)

// A task can put a credential in an artifact path. All text fields that
// leave the runner through its host logger must use the run's redactor too.
func TestArtifactFailureLogsRedactCredentials(t *testing.T) {
	for _, mode := range []string{"rejected upload", "unreadable directory"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "unreadable directory" && (os.Geteuid() == 0 || runtime.GOOS == "windows") {
				t.Skip("permission-error fixture requires an unprivileged Unix runner")
			}
			credential := "synthetic-artifact-credential-" + strings.Repeat("q7", 16)
			artifactPath := credential + ".txt"
			var host bytes.Buffer
			h := newHarness(t, func(o *Options) { o.Logger = slog.New(slog.NewJSONHandler(&host, nil)) })
			h.r.reportTimeout = time.Minute
			h.r.retryBase = 10 * time.Millisecond
			h.r.retryMax = 10 * time.Millisecond
			if mode == "unreadable directory" {
				h.r.reportTimeout = 150 * time.Millisecond
			}
			attempts := 0
			var receivedPath string
			if mode == "rejected upload" {
				h.r.coord = &artifactAttemptCoordinator{fakeCoordinator: h.coord, attempt: func(_ context.Context, _ string, _ uuid.UUID, a runproto.Artifact) error {
					attempts++
					receivedPath = a.Path
					body, err := io.ReadAll(a.Content)
					if err != nil || string(body) != "synthetic artifact" {
						t.Error("coordinator did not receive the complete original artifact")
					}
					return fmt.Errorf("%w: artifact %s rejected", runproto.ErrRejected, a.Path)
				}}
			}
			m := pyManifest()
			m.Credentials = []runproto.CredentialRef{{Name: "LLM", As: runproto.InjectEnv}}
			a := newAssignment(m)
			h.coord.secrets[a.RunID] = []runproto.Secret{{Name: "LLM", As: runproto.InjectEnv, Fields: []runproto.SecretField{{Name: "TOKEN", Value: credential, Sensitive: true}}}}
			var blocked string
			t.Cleanup(func() {
				if blocked != "" {
					_ = os.Chmod(blocked, 0o700) // #nosec G302 -- restore owner access to a test directory for cleanup
				}
			})
			h.ex.behave = func(c *fakeContainer) {
				c.stdout("redaction check " + credential + "\n")
				out := c.mountSource(outMount)
				if mode == "rejected upload" {
					if err := os.WriteFile(filepath.Join(out, artifactPath), []byte("synthetic artifact"), 0o600); err != nil {
						t.Error("could not create artifact fixture")
					}
				} else {
					blocked = filepath.Join(out, credential)
					if err := os.Mkdir(blocked, 0o700); err != nil {
						t.Error("could not create directory fixture")
					}
					if err := os.WriteFile(filepath.Join(blocked, "body"), []byte("synthetic artifact"), 0o600); err != nil {
						t.Error("could not create child fixture")
					}
					if err := os.Chmod(blocked, 0); err != nil {
						t.Error("could not restrict directory fixture")
					}
				}
				c.exit(0, false)
			}
			executeArtifactAssignment(t, h, t.Context(), a)
			if _, ok := h.coord.finish(a.RunID); ok {
				t.Fatal("artifact delivery failure submitted a terminal report")
			}
			if !h.ex.wasRemoved("c1") || h.r.activeCount() != 0 {
				t.Error("artifact delivery failure changed container cleanup or slot release")
			}
			if mode == "rejected upload" && (attempts != 1 || receivedPath != artifactPath) {
				t.Error("redaction changed the protocol path or permanent-error retry classification")
			}
			taskText := strings.Join(h.coord.logTexts(a.RunID), "\n")
			if strings.Contains(taskText, credential) || !strings.Contains(taskText, redactedMark) {
				t.Error("task logs did not redact the injected credential")
			}
			seen := map[string]bool{}
			dec := json.NewDecoder(bytes.NewReader(host.Bytes()))
			for {
				var entry map[string]any
				err := dec.Decode(&entry)
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatal("invalid captured host JSON")
				}
				var inspect func(any)
				inspect = func(v any) {
					switch value := v.(type) {
					case string:
						if strings.Contains(value, credential) {
							t.Error("a decoded host JSON text field leaked the injected credential")
						}
					case map[string]any:
						for key, child := range value {
							inspect(key)
							inspect(child)
						}
					case []any:
						for _, child := range value {
							inspect(child)
						}
					}
				}
				inspect(entry)
				message, _ := entry["msg"].(string)
				seen[message] = true
				if mode == "rejected upload" && message == "upload artifact" {
					for _, field := range []string{"path", "err"} {
						text, _ := entry[field].(string)
						if !strings.Contains(text, redactedMark) {
							t.Errorf("upload warning %s was not redacted", field)
						}
					}
				}
				if mode == "rejected upload" && message == "could not deliver the end of the run; leaving it to its lease" || message == "could not remove the run directory" {
					text, _ := entry["err"].(string)
					if !strings.Contains(text, redactedMark) {
						t.Error("host failure warning lost its redaction marker")
					}
				}
			}
			if !seen["could not deliver the end of the run; leaving it to its lease"] {
				t.Error("reporting failure was not exercised")
			}
			if mode == "unreadable directory" && !seen["could not remove the run directory"] {
				t.Error("failed directory cleanup was not exercised")
			}
		})
	}
}
