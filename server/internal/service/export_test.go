package service

import (
	"testing"
	"time"
)

// SetCredentialRecheck shortens how long streams trust their credential
// for the duration of a test. Tests in this package do not run in
// parallel, so the shared variable is safe to change.
func SetCredentialRecheck(t *testing.T, d time.Duration) {
	old := credentialRecheck
	credentialRecheck = d
	t.Cleanup(func() { credentialRecheck = old })
}
