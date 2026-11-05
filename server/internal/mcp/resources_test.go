package mcp

import (
	"context"
	"slices"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/niltavi/lerryn/skills"
)

// Every embedded skill file is listed and readable as a resource
// (tech design 10.7), with the same content as the embedded file.
func TestSkillResources(t *testing.T) {
	srv := NewServer(loadTestRegistry(t), newStubAPI(), Options{})
	ct, st := mcpsdk.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ss.Close() }()
	cs, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()

	var uris []string
	for r, err := range cs.Resources(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		uris = append(uris, r.URI)
	}
	var want []string
	list, err := skills.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		for _, f := range s.Files {
			want = append(want, SkillURI(s.Name, f))
		}
	}
	slices.Sort(uris)
	slices.Sort(want)
	if !slices.Equal(uris, want) {
		t.Fatalf("resources = %v\nwant %v", uris, want)
	}

	uri := "lerryn://skills/lerryn-automation/SKILL.md"
	res, err := cs.ReadResource(ctx, &mcpsdk.ReadResourceParams{URI: uri})
	if err != nil {
		t.Fatal(err)
	}
	content, _ := skills.ReadFile("lerryn-automation", "SKILL.md")
	if len(res.Contents) != 1 || res.Contents[0].Text != string(content) || res.Contents[0].MIMEType != "text/markdown" {
		t.Fatalf("read %s = %+v", uri, res.Contents)
	}
	if _, err := cs.ReadResource(ctx, &mcpsdk.ReadResourceParams{URI: "lerryn://skills/lerryn-automation/../../etc/passwd"}); err == nil {
		t.Fatal("reading an unknown resource succeeded")
	}
}
