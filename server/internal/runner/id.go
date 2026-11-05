package runner

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// idFile holds the runner's ID inside its data directory.
const idFile = "runner-id"

// LoadID returns the runner ID stored in dataDir, creating it with a new
// random ID on the first start. Containers are labelled with the ID, so
// keeping it across restarts is what lets a restarted runner find and stop
// the containers an earlier instance left behind (tech design 7.6).
func LoadID(dataDir string) (string, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return "", fmt.Errorf("create runner data dir: %w", err)
	}
	path := filepath.Join(dataDir, idFile)
	id := uuid.NewString()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- dataDir is configuration
	switch {
	case err == nil:
		_, werr := f.WriteString(id + "\n")
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			_ = os.Remove(path)
			return "", fmt.Errorf("write runner id: %w", werr)
		}
		return id, nil
	case errors.Is(err, fs.ErrExist):
		data, err := os.ReadFile(path) // #nosec G304 -- dataDir is configuration
		if err != nil {
			return "", fmt.Errorf("read runner id: %w", err)
		}
		id = strings.TrimSpace(string(data))
		if !validID.MatchString(id) {
			return "", fmt.Errorf("runner id in %s is invalid: %q", path, id)
		}
		return id, nil
	default:
		return "", fmt.Errorf("create runner id: %w", err)
	}
}
