package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"

	"github.com/niltavi/lerryn/server/internal/executor"
	"github.com/niltavi/lerryn/server/internal/runproto"
)

// Credential injection (tech design 12.5). The runner fetches the run's
// credentials while preparing it, keeps them in memory only, and writes
// them to the container's stdin, where sbx-exec, the container's
// entrypoint, installs them before it execs the task. The same values
// become the run's redaction patterns.

// secretsHeader and secretsEnd frame the stdin payload; see
// runtimes/sdk/sh/sbx-exec for the format.
const (
	secretsHeader = "LERRYN-SECRETS 1\n" // #nosec G101 -- a protocol marker, not a credential
	secretsEnd    = "END\n"
	// maxSecretValue matches sbx-exec's limit on one value.
	maxSecretValue = 128 << 10
	// secretsDir is where file credentials go: the executor's tmpfs.
	secretsDir = executor.SecretsDir
	// sbxExec is the container entrypoint in every runtime's SDK tree.
	sbxExec = sdkMount + "/bin/sbx-exec"
)

// secretName matches reference and field names, as the server validates
// them; anything else would break the payload's framing.
var secretName = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// runSecrets is what the runner holds for one run.
type runSecrets struct {
	payload []byte
	values  []string
}

// fetchSecrets asks the server for the run's credentials. A credential the
// run cannot have fails the run with the server's error code before any
// task code runs.
func (x *runExec) fetchSecrets(ctx, prepCtx context.Context) (*runSecrets, *verdict) {
	var secrets []runproto.Secret
	err := x.r.call(prepCtx, 3, func(ctx context.Context) error {
		var err error
		secrets, err = x.r.coord.Secrets(ctx, x.r.opts.ID, x.run.a.RunID)
		return err
	})
	var ce *runproto.CredentialError
	if errors.As(err, &ce) {
		code := ce.Code
		if code != runproto.CodeCredentialMissing {
			code = runproto.CodeCredentialUnavailable
		}
		return nil, x.fail(code, ce.Message)
	}
	if v := x.prepFailed(ctx, err, "fetch the run's credentials"); v != nil {
		return nil, v
	}
	rs, err := buildSecrets(secrets)
	if err != nil {
		return nil, x.fail(runproto.CodeCredentialUnavailable, err.Error())
	}
	x.logs.useSecrets(newRedactor(rs.values))
	return rs, nil
}

// buildSecrets encodes secrets as sbx-exec's stdin payload. Errors never
// quote a value.
func buildSecrets(secrets []runproto.Secret) (*runSecrets, error) {
	var b bytes.Buffer
	b.WriteString(secretsHeader)
	rs := &runSecrets{}
	env := map[string]string{}
	files := map[string]bool{}
	for _, s := range secrets {
		if !secretName.MatchString(s.Name) {
			return nil, fmt.Errorf("credential reference %q is not a valid name", s.Name)
		}
		if s.As != runproto.InjectEnv && s.As != runproto.InjectFile {
			return nil, fmt.Errorf("credential %s has unknown injection mode %q", s.Name, s.As)
		}
		for _, f := range s.Fields {
			if !secretName.MatchString(f.Name) {
				return nil, fmt.Errorf("credential %s has an invalid field name", s.Name)
			}
			if len(f.Value) > maxSecretValue || bytes.IndexByte([]byte(f.Value), 0) >= 0 {
				return nil, fmt.Errorf("field %s of credential %s cannot be injected", f.Name, s.Name)
			}
			if s.As == runproto.InjectEnv {
				if other, ok := env[f.Name]; ok {
					return nil, fmt.Errorf("credentials %s and %s both set the environment variable %s", other, s.Name, f.Name)
				}
				env[f.Name] = s.Name
			} else {
				key := s.Name + "/" + f.Name
				if files[key] {
					return nil, fmt.Errorf("credential %s lists field %s twice", s.Name, f.Name)
				}
				files[key] = true
			}
			b.WriteString(s.As + " " + s.Name + " " + f.Name + " " + strconv.Itoa(len(f.Value)) + "\n")
			b.WriteString(f.Value)
			b.WriteByte('\n')
			if f.Sensitive {
				rs.values = append(rs.values, f.Value)
			}
		}
	}
	b.WriteString(secretsEnd)
	if b.Len() > executor.MaxStdinBytes {
		clear(b.Bytes())
		return nil, fmt.Errorf("the run's credentials are larger than %s", formatBytes(executor.MaxStdinBytes))
	}
	rs.payload = b.Bytes()
	return rs, nil
}

func isCredentialError(err error) bool {
	var ce *runproto.CredentialError
	return errors.As(err, &ce)
}

// wipe clears the payload once the container has read it. The values
// stay with the redactor for the rest of the run.
func (rs *runSecrets) wipe() {
	if rs != nil {
		clear(rs.payload)
		rs.payload = nil
	}
}
