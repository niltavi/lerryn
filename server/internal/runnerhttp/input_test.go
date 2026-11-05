package runnerhttp_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

// A run's input (a webhook body) is handed out only to the runner holding
// the run, and only while it is claimed; the claim tells the runner there
// is one.
func TestInputOverHTTP(t *testing.T) {
	e := newEnv(t)
	owner := e.client(t.TempDir(), staticToken)
	other := e.client(t.TempDir(), staticToken)
	if err := owner.Register(e.ctx, runproto.Registration{ID: "owner", Name: "a", Slots: 2}); err != nil {
		t.Fatal(err)
	}
	if err := other.Register(e.ctx, runproto.Registration{ID: "other", Name: "b", Slots: 1}); err != nil {
		t.Fatal(err)
	}

	body := []byte(`{"lead":1}`)
	withInput := e.startRun()
	key := blob.Sum(body)
	if _, err := e.db.NewUpdate().Model((*store.Run)(nil)).
		Set("input_key = ?", key).Set("input_content_type = ?", "application/json").
		Where("id = ?", withInput).Exec(e.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.blobs.Put(e.ctx, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}

	if _, err := owner.Input(e.ctx, "owner", withInput); !errors.Is(err, runproto.ErrLeaseLost) {
		t.Fatalf("input before claim: %v", err)
	}
	as, err := owner.Claim(e.ctx, "owner", 2)
	if err != nil || len(as) != 1 || !as[0].HasInput {
		t.Fatalf("claim = %+v, %v", as, err)
	}
	if _, err := other.Input(e.ctx, "owner", withInput); !errors.Is(err, runproto.ErrLeaseLost) {
		t.Fatalf("another runner reads the input: %v", err)
	}
	in, err := owner.Input(e.ctx, "owner", withInput)
	if err != nil || in.ContentType != "application/json" || !bytes.Equal(in.Content, body) {
		t.Fatalf("input = %+v, %v", in, err)
	}

	// A run without input says so at claim, and asking anyway is refused
	// for good.
	var first store.Run
	if err := e.db.NewSelect().Model(&first).Where("id = ?", withInput).Scan(e.ctx); err != nil {
		t.Fatal(err)
	}
	second, err := e.runs.Start(e.ctx, e.user, first.TaskID, service.StartRunInput{})
	if err != nil {
		t.Fatal(err)
	}
	plain := second.Run.ID
	as, err = owner.Claim(e.ctx, "owner", 1)
	if err != nil || len(as) != 1 || as[0].RunID != plain || as[0].HasInput {
		t.Fatalf("claim = %+v, %v", as, err)
	}
	if _, err := owner.Input(e.ctx, "owner", plain); !errors.Is(err, runproto.ErrRejected) {
		t.Fatalf("input of a run without one: %v", err)
	}

	// A stored body that does not match its key is not handed out.
	if _, err := e.db.NewUpdate().Model((*store.Run)(nil)).
		Set("input_key = ?", blob.Sum([]byte("other"))).Where("id = ?", plain).Exec(e.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Input(e.ctx, "owner", plain); err == nil {
		t.Fatal("input with a missing blob succeeded")
	}
}
