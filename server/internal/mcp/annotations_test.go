package mcp

import (
	"net/http"
	"testing"
)

func TestAnnotations(t *testing.T) {
	tests := []struct {
		kind                                         OperationKind
		method                                       string
		readOnly, destructive, idempotent, openWorld bool
	}{
		{KindRead, http.MethodGet, true, false, true, false},
		{KindRead, http.MethodPost, true, false, true, false}, // validateTaskVersion
		{KindMutate, http.MethodPost, false, false, false, false},
		{KindMutate, http.MethodPut, false, false, true, false},
		{KindMutate, http.MethodPatch, false, false, false, false},
		{KindMutate, http.MethodDelete, false, true, true, false},
		{KindExecute, http.MethodPost, false, true, false, true},
		{KindExecute, http.MethodPut, false, true, false, true},
	}
	for _, tt := range tests {
		t.Run(string(tt.kind)+" "+tt.method, func(t *testing.T) {
			a, err := annotations(tt.kind, tt.method)
			if err != nil {
				t.Fatal(err)
			}
			if a.ReadOnlyHint != tt.readOnly || *a.DestructiveHint != tt.destructive ||
				a.IdempotentHint != tt.idempotent || *a.OpenWorldHint != tt.openWorld {
				t.Fatalf("got readOnly=%v destructive=%v idempotent=%v openWorld=%v",
					a.ReadOnlyHint, *a.DestructiveHint, a.IdempotentHint, *a.OpenWorldHint)
			}
		})
	}
}

// setTriggers replaces the whole trigger set with a PUT, so the table says
// non-destructive; its x-mcp.hints corrects that.
func TestWithHints(t *testing.T) {
	a, err := annotations(KindMutate, http.MethodPut)
	if err != nil {
		t.Fatal(err)
	}
	a = withHints(a, &xHints{Destructive: new(true), OpenWorld: new(true)})
	if !*a.DestructiveHint || !a.IdempotentHint || !*a.OpenWorldHint {
		t.Fatalf("got destructive=%v idempotent=%v openWorld=%v", *a.DestructiveHint, a.IdempotentHint, *a.OpenWorldHint)
	}
	if got := withHints(a, nil); got != a {
		t.Fatal("nil hints must leave the annotations unchanged")
	}
}

func TestAnnotationsRejectContradictions(t *testing.T) {
	for _, tt := range []struct {
		kind   OperationKind
		method string
	}{
		{KindRead, http.MethodDelete},
		{KindRead, http.MethodPut},
		{KindMutate, http.MethodGet},
		{KindExecute, http.MethodGet},
		{KindRead, http.MethodOptions},
	} {
		if _, err := annotations(tt.kind, tt.method); err == nil {
			t.Errorf("%s %s: want error", tt.kind, tt.method)
		}
	}
}
