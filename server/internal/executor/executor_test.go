package executor

import (
	"errors"
	"strings"
	"testing"
)

func validSpec() Spec {
	return Spec{
		RunID:    "0192f1a2-0000-7000-8000-000000000001",
		RunnerID: "runner-1",
		Image:    "lerryn-runtime-python@sha256:0000",
		Command:  []string{"python", "main.py"},
		Network:  NetworkNone,
	}
}

func TestSpecValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Spec)
		wantErr string // substring; empty means valid
	}{
		{"minimal", func(*Spec) {}, ""},
		{"full network", func(s *Spec) { s.Network = NetworkFull }, ""},
		{"explicit non-root user", func(s *Spec) { s.User = "1000:1000" }, ""},
		{"mounts", func(s *Spec) {
			s.Mounts = []Mount{
				{Source: "/var/lib/lerryn/runs/1/task", Target: "/sbx/task", ReadOnly: true},
				{Source: "/var/lib/lerryn/runs/1/work", Target: "/sbx/work"},
			}
		}, ""},

		{"missing run id", func(s *Spec) { s.RunID = "" }, "run id"},
		{"run id with slash", func(s *Spec) { s.RunID = "a/b" }, "run id"},
		{"missing runner id", func(s *Spec) { s.RunnerID = "" }, "runner id"},
		{"missing image", func(s *Spec) { s.Image = " " }, "image is required"},
		{"missing command", func(s *Spec) { s.Command = nil }, "command is required"},
		{"empty argv0", func(s *Spec) { s.Command = []string{""} }, "command is required"},
		{"bad env", func(s *Spec) { s.Env = []string{"NOVALUE"} }, "KEY=VALUE"},
		{"relative workdir", func(s *Spec) { s.WorkingDir = "work" }, "working dir"},
		{"unknown network", func(s *Spec) { s.Network = "host" }, "network"},
		{"empty network", func(s *Spec) { s.Network = "" }, "network"},
		{"negative grace", func(s *Spec) { s.StopGrace = -1 }, "stop grace"},

		{"root by name", func(s *Spec) { s.User = "root" }, "numeric"},
		{"root uid", func(s *Spec) { s.User = "0:1000" }, "must not run as root"},
		{"root uid alone", func(s *Spec) { s.User = "0" }, "must not run as root"},
		{"root gid", func(s *Spec) { s.User = "1000:0" }, "root group"},
		{"uid without gid", func(s *Spec) { s.User = "1000" }, "must include a gid"},
		{"uid overflow", func(s *Spec) { s.User = "99999999999:1" }, "uid"},

		{"memory in MiB by mistake", func(s *Spec) { s.Resources.MemoryBytes = 512 }, "memory"},
		{"negative pids", func(s *Spec) { s.Resources.PIDs = -1 }, "pids"},
		{"tiny cpu", func(s *Spec) { s.Resources.MilliCPUs = 1 }, "cpu"},

		{"relative mount source", func(s *Spec) { s.Mounts = []Mount{{Source: "data", Target: "/sbx/work"}} }, "mount source"},
		{"unclean mount source", func(s *Spec) {
			s.Mounts = []Mount{{Source: "/var/lib/lerryn/../../etc", Target: "/sbx/work"}}
		}, "clean absolute"},
		{"root mount source", func(s *Spec) { s.Mounts = []Mount{{Source: "/", Target: "/host"}} }, "must not be /"},
		{"docker socket", func(s *Spec) {
			s.Mounts = []Mount{{Source: "/var/run/docker.sock", Target: "/var/run/docker.sock"}}
		}, "runtime socket"},
		{"podman socket", func(s *Spec) {
			s.Mounts = []Mount{{Source: "/run/user/1000/podman/podman.sock", Target: "/sock"}}
		}, "runtime socket"},
		{"socket directory", func(s *Spec) { s.Mounts = []Mount{{Source: "/var/run", Target: "/hostrun"}} }, "runtime socket"},
		{"over /tmp", func(s *Spec) { s.Mounts = []Mount{{Source: "/data", Target: "/tmp"}} }, "reserved"},
		{"under secrets", func(s *Spec) { s.Mounts = []Mount{{Source: "/data", Target: "/run/secrets/x"}} }, "reserved"},
		{"above secrets", func(s *Spec) { s.Mounts = []Mount{{Source: "/data", Target: "/run"}} }, "reserved"},
		{"over proc", func(s *Spec) { s.Mounts = []Mount{{Source: "/data", Target: "/proc/sys"}} }, "reserved"},
		{"over root", func(s *Spec) { s.Mounts = []Mount{{Source: "/data", Target: "/"}} }, "reserved"},
		{"duplicate target", func(s *Spec) {
			s.Mounts = []Mount{{Source: "/a", Target: "/sbx/work"}, {Source: "/b", Target: "/sbx/work"}}
		}, "used twice"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := validSpec()
			tc.mutate(&s)
			err := s.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want error containing %q", tc.wantErr)
			}
			if !errors.Is(err, ErrInvalidSpec) {
				t.Errorf("error %v does not wrap ErrInvalidSpec", err)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Validate() = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestSpecValidateReportsAllProblems(t *testing.T) {
	s := validSpec()
	s.Image = ""
	s.User = "0:0"
	s.Network = "bridge"
	err := s.Validate()
	for _, want := range []string{"image", "root", "network"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Validate() = %v, want it to mention %q", err, want)
		}
	}
}

func TestResourcesWithDefaults(t *testing.T) {
	got := Resources{MemoryBytes: 256 << 20}.WithDefaults()
	want := Resources{
		MilliCPUs:    DefaultMilliCPUs,
		MemoryBytes:  256 << 20,
		PIDs:         DefaultPIDs,
		OpenFiles:    DefaultOpenFiles,
		TmpBytes:     DefaultTmpBytes,
		SecretsBytes: DefaultSecretsBytes,
	}
	if got != want {
		t.Fatalf("WithDefaults() = %+v, want %+v", got, want)
	}
}
