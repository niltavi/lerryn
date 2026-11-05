package docker

import (
	"context"
	"errors"
	"strings"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/niltavi/lerryn/server/internal/executor"
)

// fakeNetworks answers network calls. inspects are the answers to
// successive NetworkInspect calls; a nil entry means the network does not
// exist. Unused methods of api panic through the nil embedded interface.
type fakeNetworks struct {
	api

	inspects  []*network.Inspect
	createErr error
	created   []client.NetworkCreateOptions
}

func (f *fakeNetworks) NetworkInspect(_ context.Context, name string, _ client.NetworkInspectOptions) (client.NetworkInspectResult, error) {
	if len(f.inspects) == 0 {
		return client.NetworkInspectResult{}, errors.New("unexpected inspect of " + name)
	}
	n := f.inspects[0]
	f.inspects = f.inspects[1:]
	if n == nil {
		return client.NetworkInspectResult{}, cerrdefs.ErrNotFound
	}
	return client.NetworkInspectResult{Network: *n}, nil
}

func (f *fakeNetworks) NetworkCreate(_ context.Context, _ string, opts client.NetworkCreateOptions) (client.NetworkCreateResult, error) {
	f.created = append(f.created, opts)
	return client.NetworkCreateResult{}, f.createErr
}

func taskNetwork(driver string, options map[string]string) *network.Inspect {
	var n network.Inspect
	n.Name = DefaultNetworkName
	n.Driver = driver
	n.Options = options
	return &n
}

func TestEnsureNetwork(t *testing.T) {
	isolated := map[string]string{networkOptionICC: "false"}
	tests := []struct {
		name      string
		inspects  []*network.Inspect
		createErr error
		created   bool
		// wantErr is a part of the hardening error; empty means success.
		wantErr string
	}{
		{name: "missing network is created", inspects: []*network.Inspect{nil}, created: true},
		{name: "existing isolated network", inspects: []*network.Inspect{taskNetwork("bridge", isolated)}},
		{name: "existing network with icc", inspects: []*network.Inspect{taskNetwork("bridge", nil)}, wantErr: "enable_icc=false"},
		{name: "existing network with icc on", inspects: []*network.Inspect{taskNetwork("bridge", map[string]string{networkOptionICC: "true"})}, wantErr: "enable_icc=false"},
		{name: "existing network of another driver", inspects: []*network.Inspect{taskNetwork("macvlan", isolated)}, wantErr: `"macvlan" driver`},
		{name: "created concurrently and isolated", inspects: []*network.Inspect{nil, taskNetwork("bridge", isolated)},
			createErr: cerrdefs.ErrConflict, created: true},
		{name: "created concurrently without isolation", inspects: []*network.Inspect{nil, taskNetwork("bridge", nil)},
			createErr: cerrdefs.ErrConflict, created: true, wantErr: "enable_icc=false"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := &fakeNetworks{inspects: tc.inspects, createErr: tc.createErr}
			e, err := newWithAPI(d, Options{})
			if err != nil {
				t.Fatal(err)
			}
			err = e.ensureNetwork(t.Context())
			if created := len(d.created) > 0; created != tc.created {
				t.Fatalf("created = %v, want %v", created, tc.created)
			}
			if tc.created && d.created[0].Options[networkOptionICC] != "false" {
				t.Fatalf("created with options %v", d.created[0].Options)
			}
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ensureNetwork() = %v", err)
				}
				if !e.netReady {
					t.Fatal("network not marked ready")
				}
				return
			}
			if !errors.Is(err, executor.ErrHardening) || !strings.Contains(err.Error(), tc.wantErr) ||
				!strings.Contains(err.Error(), "docker network rm "+DefaultNetworkName) {
				t.Fatalf("ensureNetwork() = %v, want ErrHardening mentioning %q and how to remove the network", err, tc.wantErr)
			}
			if e.netReady {
				t.Fatal("a rejected network was marked ready")
			}
		})
	}
}
