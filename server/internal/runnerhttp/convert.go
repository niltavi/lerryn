package runnerhttp

import (
	"time"

	"github.com/niltavi/lerryn/server/internal/runproto"
)

// Conversions between runproto values and their wire form. Durations
// travel as integer milliseconds.

func toWireAssignment(a runproto.Assignment) assignment {
	m := a.Manifest
	stages := make([]stage, len(m.Stages))
	for i, s := range m.Stages {
		stages[i] = stage(s)
	}
	return assignment{
		RunID:     a.RunID,
		TaskID:    a.TaskID,
		VersionID: a.VersionID,
		Params:    a.Params,
		HasInput:  a.HasInput,
		HasState:  a.HasState,
		Manifest: manifest{
			Language:      m.Runtime.Language,
			Version:       m.Runtime.Version,
			Lockfile:      m.Runtime.Lockfile,
			Entrypoint:    m.Entrypoint,
			Stages:        stages,
			TimeoutMillis: m.Timeout.Milliseconds(),
			MilliCPUs:     m.MilliCPUs,
			MemoryBytes:   m.MemoryBytes,
			Network:       m.Network,
			Credentials:   toWireCredentialRefs(m.Credentials),
			Tools:         convertTools(m.Tools, toWireTool),
		},
	}
}

func convertTools[A, B any](in []A, f func(A) B) []B {
	if len(in) == 0 {
		return nil
	}
	out := make([]B, len(in))
	for i, t := range in {
		out[i] = f(t)
	}
	return out
}

func toWireTool(t runproto.Tool) tool {
	return tool{Name: t.Name, Kind: t.Kind, URL: t.URL, TreeHash: t.TreeHash,
		Files: convertTools(t.Files, func(f runproto.File) file { return file(f) })}
}

func fromWireTool(t tool) runproto.Tool {
	return runproto.Tool{Name: t.Name, Kind: t.Kind, URL: t.URL, TreeHash: t.TreeHash,
		Files: convertTools(t.Files, func(f file) runproto.File { return runproto.File(f) })}
}

func toWireCredentialRefs(refs []runproto.CredentialRef) []credentialRef {
	if len(refs) == 0 {
		return nil
	}
	out := make([]credentialRef, len(refs))
	for i, r := range refs {
		out[i] = credentialRef(r)
	}
	return out
}

func fromWireCredentialRefs(refs []credentialRef) []runproto.CredentialRef {
	if len(refs) == 0 {
		return nil
	}
	out := make([]runproto.CredentialRef, len(refs))
	for i, r := range refs {
		out[i] = runproto.CredentialRef(r)
	}
	return out
}

func toWireSecrets(secrets []runproto.Secret) []secret {
	out := make([]secret, len(secrets))
	for i, s := range secrets {
		fields := make([]secretField, len(s.Fields))
		for j, f := range s.Fields {
			fields[j] = secretField(f)
		}
		out[i] = secret{Name: s.Name, As: s.As, Fields: fields}
	}
	return out
}

func fromWireSecrets(secrets []secret) []runproto.Secret {
	out := make([]runproto.Secret, len(secrets))
	for i, s := range secrets {
		fields := make([]runproto.SecretField, len(s.Fields))
		for j, f := range s.Fields {
			fields[j] = runproto.SecretField(f)
		}
		out[i] = runproto.Secret{Name: s.Name, As: s.As, Fields: fields}
	}
	return out
}

func fromWireAssignment(a assignment) runproto.Assignment {
	m := a.Manifest
	stages := make([]runproto.Stage, len(m.Stages))
	for i, s := range m.Stages {
		stages[i] = runproto.Stage(s)
	}
	return runproto.Assignment{
		RunID:     a.RunID,
		TaskID:    a.TaskID,
		VersionID: a.VersionID,
		Params:    a.Params,
		HasInput:  a.HasInput,
		HasState:  a.HasState,
		Manifest: runproto.Manifest{
			Runtime:     runproto.Runtime{Language: m.Language, Version: m.Version, Lockfile: m.Lockfile},
			Entrypoint:  m.Entrypoint,
			Stages:      stages,
			Timeout:     time.Duration(m.TimeoutMillis) * time.Millisecond,
			MilliCPUs:   m.MilliCPUs,
			MemoryBytes: m.MemoryBytes,
			Network:     m.Network,
			Credentials: fromWireCredentialRefs(m.Credentials),
			Tools:       convertTools(m.Tools, fromWireTool),
		},
	}
}

func toWireLines(lines []runproto.LogLine) []logLine {
	out := make([]logLine, len(lines))
	for i, l := range lines {
		out[i] = logLine(l)
	}
	return out
}

func fromWireLines(lines []logLine) []runproto.LogLine {
	out := make([]runproto.LogLine, len(lines))
	for i, l := range lines {
		out[i] = runproto.LogLine(l)
	}
	return out
}

func toWireEvents(events []runproto.Event) []event {
	out := make([]event, len(events))
	for i, e := range events {
		out[i] = event{Seq: e.Seq, TS: e.TS, Kind: e.Kind, Stage: e.Stage, Message: e.Message}
		if e.Result != nil {
			out[i].Result = &result{Status: e.Result.Status, Summary: e.Result.Summary, Data: e.Result.Data}
		}
		if c := e.ToolCall; c != nil {
			wc := toolCall(*c)
			out[i].ToolCall = &wc
		}
	}
	return out
}

func fromWireEvents(events []event) []runproto.Event {
	out := make([]runproto.Event, len(events))
	for i, e := range events {
		out[i] = runproto.Event{Seq: e.Seq, TS: e.TS, Kind: e.Kind, Stage: e.Stage, Message: e.Message}
		if e.Result != nil {
			out[i].Result = &runproto.Result{Status: e.Result.Status, Summary: e.Result.Summary, Data: e.Result.Data}
		}
		if c := e.ToolCall; c != nil {
			rc := runproto.ToolCall(*c)
			out[i].ToolCall = &rc
		}
	}
	return out
}
