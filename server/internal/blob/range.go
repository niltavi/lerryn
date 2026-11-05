package blob

import (
	"context"
	"fmt"
	"io"
	"os"
)

// RangeReader is implemented by stores that can read part of an object
// without transferring the rest, such as both stores in this package.
type RangeReader interface {
	// GetRange opens length bytes of the object starting at offset. The
	// caller closes it.
	GetRange(ctx context.Context, hash string, offset, length int64) (io.ReadCloser, error)
}

// ReadRange returns length bytes of the object with the given hash,
// starting at offset. Stores without range reads read up to the range and
// discard what comes before it.
func ReadRange(ctx context.Context, s Store, hash string, offset, length int64) ([]byte, error) {
	if offset < 0 || length < 0 {
		return nil, fmt.Errorf("read blob %s: invalid range %d+%d", hash, offset, length)
	}
	var rc io.ReadCloser
	var err error
	if rr, ok := s.(RangeReader); ok {
		rc, err = rr.GetRange(ctx, hash, offset, length)
	} else if rc, err = s.Get(ctx, hash); err == nil {
		if _, err = io.CopyN(io.Discard, rc, offset); err != nil {
			_ = rc.Close()
			return nil, fmt.Errorf("read blob %s: skip to %d: %w", hash, offset, err)
		}
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	data := make([]byte, length)
	if _, err := io.ReadFull(rc, data); err != nil {
		return nil, fmt.Errorf("read blob %s at %d+%d: %w", hash, offset, length, err)
	}
	return data, nil
}

// GetRange implements RangeReader.
func (l *Local) GetRange(ctx context.Context, hash string, offset, length int64) (io.ReadCloser, error) {
	rc, err := l.Get(ctx, hash)
	if err != nil {
		return nil, err
	}
	f, ok := rc.(*os.File)
	if !ok {
		_ = rc.Close()
		return nil, fmt.Errorf("blob: open %s: not a file", hash)
	}
	return struct {
		io.Reader
		io.Closer
	}{io.NewSectionReader(f, offset, length), f}, nil
}
