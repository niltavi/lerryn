package runproto

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"
	"unicode/utf8"
)

// StateProtocolVersion is the first runner protocol with task state.
const StateProtocolVersion = 2

// State limits bound both the file set and its JSON/base64 representation.
const (
	StateFormatVersion = 1
	MaxStateBytes      = 10 << 20
	MaxStateEntries    = 1000
	MaxStatePathBytes  = 255
	MaxStateWireBytes  = 16 << 20
)

// StateSnapshot is a complete task directory, including empty directories.
// It contains no timestamps, owners, symlinks or privileged mode bits.
type StateSnapshot struct {
	Version int          `json:"version"`
	Entries []StateEntry `json:"entries"`
}

// StateEntry names a clean UTF-8 relative path. Every parent directory
// must have its own entry; only regular files can have content or execute.
type StateEntry struct {
	Path       string `json:"path"`
	Directory  bool   `json:"directory,omitempty"`
	Executable bool   `json:"executable,omitempty"`
	Content    []byte `json:"content,omitempty"`
}

// EncodeState validates and canonically encodes a snapshot without changing
// the caller's entry order. Size is the total unencoded file content.
func EncodeState(snapshot StateSnapshot) (encoded []byte, size int64, err error) {
	if snapshot.Version != StateFormatVersion {
		return nil, 0, errors.New("unsupported task state format")
	}
	if len(snapshot.Entries) > MaxStateEntries {
		return nil, 0, errors.New("task state has too many entries")
	}
	entries := slices.Clone(snapshot.Entries)
	if entries == nil {
		entries = []StateEntry{}
	}
	slices.SortFunc(entries, func(a, b StateEntry) int { return strings.Compare(a.Path, b.Path) })
	seen := make(map[string]bool, len(entries))
	for i, entry := range entries {
		if !validStatePath(entry.Path) {
			return nil, 0, fmt.Errorf("task state entry %d has an invalid relative path", i)
		}
		if _, exists := seen[entry.Path]; exists {
			return nil, 0, fmt.Errorf("task state entry %d repeats a path", i)
		}
		if parent := path.Dir(entry.Path); parent != "." {
			if directory, exists := seen[parent]; !exists || !directory {
				return nil, 0, fmt.Errorf("task state entry %d has no directory parent", i)
			}
		}
		if entry.Directory && (entry.Executable || len(entry.Content) != 0) {
			return nil, 0, fmt.Errorf("task state directory entry %d has file attributes", i)
		}
		if int64(len(entry.Content)) > int64(MaxStateBytes)-size {
			return nil, 0, errors.New("task state exceeds its file content limit")
		}
		size += int64(len(entry.Content))
		seen[entry.Path] = entry.Directory
	}
	encoded, err = json.Marshal(StateSnapshot{Version: StateFormatVersion, Entries: entries})
	if err != nil {
		return nil, 0, fmt.Errorf("encode task state: %w", err)
	}
	if len(encoded) > MaxStateWireBytes {
		return nil, 0, errors.New("task state exceeds its encoded limit")
	}
	return encoded, size, nil
}

// DecodeState rejects missing entries rather than treating an incomplete
// response as a successful empty snapshot. Errors do not echo task paths
// or file contents, which can contain private data.
func DecodeState(encoded []byte) (StateSnapshot, int64, error) {
	if len(encoded) > MaxStateWireBytes {
		return StateSnapshot{}, 0, errors.New("task state exceeds its encoded limit")
	}
	if !utf8.Valid(encoded) {
		return StateSnapshot{}, 0, errors.New("task state JSON is not UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return StateSnapshot{}, 0, errors.New("invalid task state JSON")
	}
	snapshot := StateSnapshot{Entries: []StateEntry{}}
	var sawVersion, sawEntries bool
	for decoder.More() {
		field, err := decoder.Token()
		if err != nil {
			return StateSnapshot{}, 0, errors.New("invalid task state JSON")
		}
		switch field {
		case "version":
			if sawVersion || decoder.Decode(&snapshot.Version) != nil {
				return StateSnapshot{}, 0, errors.New("invalid task state version")
			}
			sawVersion = true
		case "entries":
			if sawEntries {
				return StateSnapshot{}, 0, errors.New("task state repeats entries")
			}
			sawEntries = true
			open, err := decoder.Token()
			if err != nil || open != json.Delim('[') {
				return StateSnapshot{}, 0, errors.New("task state entries must be an array")
			}
			for decoder.More() {
				// Check before decoding, so millions of tiny entries cannot
				// allocate a large slice before the entry limit is applied.
				if len(snapshot.Entries) == MaxStateEntries {
					return StateSnapshot{}, 0, errors.New("task state has too many entries")
				}
				var entry StateEntry
				if decoder.Decode(&entry) != nil {
					return StateSnapshot{}, 0, errors.New("invalid task state entry")
				}
				snapshot.Entries = append(snapshot.Entries, entry)
			}
			if close, err := decoder.Token(); err != nil || close != json.Delim(']') {
				return StateSnapshot{}, 0, errors.New("invalid task state entries")
			}
		default:
			return StateSnapshot{}, 0, errors.New("unknown task state field")
		}
	}
	if close, err := decoder.Token(); err != nil || close != json.Delim('}') || !sawVersion || !sawEntries {
		return StateSnapshot{}, 0, errors.New("incomplete task state JSON")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return StateSnapshot{}, 0, errors.New("task state has trailing JSON")
	}
	_, size, err := EncodeState(snapshot)
	if err != nil {
		return StateSnapshot{}, 0, err
	}
	slices.SortFunc(snapshot.Entries, func(a, b StateEntry) int { return strings.Compare(a.Path, b.Path) })
	return snapshot, size, nil
}

func validStatePath(name string) bool {
	if name == "." || len(name) > MaxStatePathBytes || !utf8.ValidString(name) || !fs.ValidPath(name) || strings.ContainsAny(name, "\\\x00") {
		return false
	}
	// A Windows drive prefix must not become an absolute destination when
	// a snapshot moves between hosts.
	if len(name) >= 2 && name[1] == ':' {
		first := name[0]
		if first >= 'A' && first <= 'Z' || first >= 'a' && first <= 'z' {
			return false
		}
	}
	return true
}
