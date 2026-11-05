package blob_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/niltavi/lerryn/server/internal/blob"
)

// plainStore hides Local's range reads, to exercise the fallback.
type plainStore struct{ blob.Store }

func TestReadRange(t *testing.T) {
	ctx := context.Background()
	local, err := blob.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hash, err := local.Put(ctx, strings.NewReader("0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]blob.Store{"local": local, "fallback": plainStore{local}} {
		got, err := blob.ReadRange(ctx, s, hash, 3, 4)
		if err != nil || string(got) != "3456" {
			t.Errorf("%s: range = %q, %v", name, got, err)
		}
		if _, err := blob.ReadRange(ctx, s, hash, 8, 4); err == nil {
			t.Errorf("%s: a range past the end was read", name)
		}
		if _, err := blob.ReadRange(ctx, s, hash, -1, 1); err == nil {
			t.Errorf("%s: a negative offset was accepted", name)
		}
	}
	rc, err := local.GetRange(ctx, hash, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(rc)
	_ = rc.Close()
	if !bytes.Equal(data, []byte("01")) {
		t.Fatalf("GetRange = %q", data)
	}
}
