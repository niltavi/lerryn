package docker

import (
	"context"
	"errors"
	"io"
	"iter"
	"strings"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/client"

	"github.com/niltavi/lerryn/server/internal/executor"
)

// imageDaemon fakes the image endpoints. Unused methods of api panic
// through the nil embedded interface.
type imageDaemon struct {
	api

	present  bool
	pullErr  error
	waitErr  error
	inspects int
	pulls    []string
}

func (d *imageDaemon) ImageInspect(_ context.Context, ref string, _ ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	d.inspects++
	if !d.present {
		return client.ImageInspectResult{}, cerrdefs.ErrNotFound.WithMessage("no such image: " + ref)
	}
	return client.ImageInspectResult{InspectResponse: image.InspectResponse{ID: "sha256:abc"}}, nil
}

func (d *imageDaemon) ImagePull(_ context.Context, ref string, _ client.ImagePullOptions) (client.ImagePullResponse, error) {
	d.pulls = append(d.pulls, ref)
	if d.pullErr != nil {
		return nil, d.pullErr
	}
	return &fakePull{onWait: func() error {
		if d.waitErr == nil {
			d.present = true
		}
		return d.waitErr
	}}, nil
}

type fakePull struct {
	onWait func() error
}

func (p *fakePull) Read([]byte) (int, error) { return 0, io.EOF }
func (p *fakePull) Close() error             { return nil }
func (p *fakePull) Wait(context.Context) error {
	return p.onWait()
}

func (p *fakePull) JSONMessages(context.Context) iter.Seq2[jsonstream.Message, error] {
	return func(func(jsonstream.Message, error) bool) {}
}

func newImageExecutor(t *testing.T, d *imageDaemon) *Executor {
	t.Helper()
	e, err := newWithAPI(d, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestEnsureImage(t *testing.T) {
	t.Run("present image is not pulled", func(t *testing.T) {
		d := &imageDaemon{present: true}
		e := newImageExecutor(t, d)
		id, err := e.EnsureImage(t.Context(), "python:3.12")
		if err != nil || id != "sha256:abc" {
			t.Fatalf("EnsureImage() = %q, %v", id, err)
		}
		if len(d.pulls) != 0 {
			t.Fatalf("pulled %v although the image was present", d.pulls)
		}
	})
	t.Run("missing image is pulled", func(t *testing.T) {
		d := &imageDaemon{}
		e := newImageExecutor(t, d)
		id, err := e.EnsureImage(t.Context(), "python:3.12")
		if err != nil || id != "sha256:abc" {
			t.Fatalf("EnsureImage() = %q, %v", id, err)
		}
		if len(d.pulls) != 1 || d.pulls[0] != "python:3.12" || d.inspects != 2 {
			t.Fatalf("pulls = %v, inspects = %d", d.pulls, d.inspects)
		}
	})
	t.Run("pull failure", func(t *testing.T) {
		d := &imageDaemon{waitErr: errors.New("manifest unknown")}
		e := newImageExecutor(t, d)
		_, err := e.EnsureImage(t.Context(), "python:nope")
		if err == nil || !strings.Contains(err.Error(), "manifest unknown") {
			t.Fatalf("EnsureImage() error = %v", err)
		}
	})
	t.Run("empty reference", func(t *testing.T) {
		d := &imageDaemon{}
		e := newImageExecutor(t, d)
		if _, err := e.EnsureImage(t.Context(), " "); !errors.Is(err, executor.ErrInvalidSpec) {
			t.Fatalf("EnsureImage() error = %v", err)
		}
		if d.inspects != 0 {
			t.Fatal("daemon contacted for an empty reference")
		}
	})
}
