package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/secrets"
	"github.com/niltavi/lerryn/server/internal/store"
)

// Rejecting credential values in submissions (tech design 11.7). An agent
// never needs a credential's plaintext: the manifest references the
// credential by name and the runner injects the value. A value in a
// manifest, a task file or run parameters means the plaintext reached the
// agent, usually because a user pasted it into the conversation, and it
// would end up in the task's versions, the run's parameter snapshot and
// the API responses that return them. Such submissions are refused.
//
// Matching uses the keyed fingerprints in credential_versions.fingerprints
// (secrets.Fingerprinter), computed when a value is filled in, so a check
// costs one query and one pass over the text rather than decrypting every
// credential. Versions stored before fingerprints existed, or
// fingerprinted under a master key that has since been rotated, are
// decrypted once and their fingerprints rewritten.
//
// Which credentials: those the caller owns, and those bound to the task,
// whoever owns them. Revoked credentials are left out. The error names a
// credential only by the name the caller already knows: the reference
// name of a binding, or the caller's own credential's name; never its
// owner, ID or value, and not which encoding matched.

// PlaintextCredentialError rejects a submission that contains a credential
// value. It is a validation error with one field error per place found,
// so errors.As finds the *ValidationError too.
type PlaintextCredentialError struct {
	ValidationError
	// Names are the names the matching credentials are known by, sorted.
	Names []string
}

func (e *PlaintextCredentialError) Error() string {
	return "submission contains credential values: " + strings.Join(e.Names, ", ")
}

func (e *PlaintextCredentialError) Unwrap() error { return &e.ValidationError }

// ScanText is one piece of a submission to check.
type ScanText struct {
	// Pointer is the JSON pointer reported in the field error: Base and
	// the escaped Path. Path holds the object keys and array indexes
	// below Base, unescaped.
	Base string
	Path []string
	Text string
}

// pointer is the JSON pointer of t, cut before the first key that holds a
// credential value itself: the error must not echo the value back.
func (t ScanText) pointer(m *secrets.Matcher) string {
	var b strings.Builder
	b.WriteString(t.Base)
	for _, seg := range t.Path {
		if len(m.Find(seg)) > 0 {
			break
		}
		b.WriteString("/" + escapePointer(seg))
	}
	return b.String()
}

// JSONScanTexts returns every string, key and number of a JSON value as a
// ScanText, with the pointer of the value it belongs to under base.
func JSONScanTexts(base string, raw []byte) ([]ScanText, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("decode JSON to scan: %w", err)
	}
	var out []ScanText
	var walk func(path []string, v any)
	walk = func(path []string, v any) {
		switch x := v.(type) {
		case string:
			out = append(out, ScanText{Base: base, Path: path, Text: x})
		case json.Number:
			out = append(out, ScanText{Base: base, Path: path, Text: x.String()})
		case []any:
			for i, e := range x {
				walk(append(slices.Clip(path), strconv.Itoa(i)), e)
			}
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				child := append(slices.Clip(path), k)
				out = append(out, ScanText{Base: base, Path: child, Text: k})
				walk(child, x[k])
			}
		}
	}
	walk(nil, v)
	return out, nil
}

// escapePointer escapes a JSON pointer reference token (RFC 6901).
func escapePointer(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

// fingerprintRecord is the JSON of credential_versions.fingerprints.
type fingerprintRecord struct {
	V int `json:"v"`
	// Key is the ID of the master key the fingerprint keys derive from.
	Key   string            `json:"key"`
	Forms []fingerprintForm `json:"forms"`
}

type fingerprintForm struct {
	// P is the prefix digest, M the MAC, both hex; N the form's length.
	P string `json:"p"`
	N int    `json:"n"`
	M string `json:"m"`
}

// fingerprints returns the fingerprint record of the sensitive values.
func (c *Credentials) fingerprints(fields []store.CredentialField, values map[string]string) (string, error) {
	f, err := secrets.NewFingerprinter(c.keys)
	if err != nil {
		return "", err
	}
	rec := fingerprintRecord{V: 1, Key: f.KeyID(), Forms: []fingerprintForm{}}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !fieldSensitive(fields, name) {
			continue
		}
		for _, fp := range f.Of(values[name]) {
			var p [8]byte
			binary.BigEndian.PutUint64(p[:], fp.Prefix)
			rec.Forms = append(rec.Forms, fingerprintForm{P: hex.EncodeToString(p[:]), N: fp.Len, M: hex.EncodeToString(fp.MAC[:])})
		}
	}
	b, err := json.Marshal(rec)
	return string(b), err
}

func parseFingerprints(raw string, keyID string) ([]secrets.Fingerprint, bool) {
	if raw == "" {
		return nil, false
	}
	var rec fingerprintRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil || rec.V != 1 || rec.Key != keyID {
		return nil, false
	}
	out := make([]secrets.Fingerprint, 0, len(rec.Forms))
	for _, f := range rec.Forms {
		p, err1 := hex.DecodeString(f.P)
		m, err2 := hex.DecodeString(f.M)
		if err1 != nil || err2 != nil || len(p) != 8 || len(m) != 16 {
			return nil, false
		}
		fp := secrets.Fingerprint{Len: f.N, Prefix: binary.BigEndian.Uint64(p)}
		copy(fp.MAC[:], m)
		out = append(out, fp)
	}
	return out, true
}

// CheckPlaintext returns a *PlaintextCredentialError when any text
// contains the value of a credential p owns or one bound to taskID
// (uuid.Nil for a task that does not exist yet). The rejection is
// audited, with the credential IDs, so repeated probing shows up.
func (c *Credentials) CheckPlaintext(ctx context.Context, p *auth.Principal, taskID uuid.UUID, texts []ScanText) error {
	return c.checkPlaintext(ctx, p, taskID, texts, "task")
}

func (c *Credentials) checkPlaintext(ctx context.Context, p *auth.Principal, taskID uuid.UUID, texts []ScanText, resourceKind string) error {
	if c == nil || len(texts) == 0 || p == nil {
		return nil
	}
	if c.keys == nil {
		// Without a master key no value was ever stored.
		return nil
	}
	f, err := secrets.NewFingerprinter(c.keys)
	if err != nil {
		return err
	}
	creds, names, err := c.plaintextCandidates(ctx, p.UserID, taskID)
	if err != nil || len(creds) == 0 {
		return err
	}
	m := f.NewMatcher()
	for i, cr := range creds {
		fps, err := c.currentFingerprints(ctx, f, cr)
		if err != nil {
			// The check is a safety net; a credential that cannot be read
			// here also cannot be used by runs, and says so there.
			c.log.WarnContext(ctx, "credential fingerprints unavailable", "credential_id", cr.ID, "error", err)
			continue
		}
		m.Add(i, fps)
	}
	if m.Empty() {
		return nil
	}
	detail := "这里含有凭证 %s 的值。凭证不能写进清单、任务文件或参数：在清单的 credentials 中引用它，任务运行时从 /run/secrets 或环境变量读取"
	if resourceKind == "ai" {
		detail = "这里含有凭证 %s 的值。请移除凭证明文"
	}
	perr := &PlaintextCredentialError{}
	perr.Content = true
	var hitIDs []string
	for _, t := range texts {
		for _, label := range m.Find(t.Text) {
			name := names[label]
			pointer := t.pointer(m)
			if slices.ContainsFunc(perr.Fields, func(f FieldError) bool { return f.Pointer == pointer && strings.Contains(f.Detail, " "+name+" ") }) {
				continue
			}
			perr.Fields = append(perr.Fields, FieldError{Pointer: pointer, Detail: fmt.Sprintf(detail, name)})
			if !slices.Contains(perr.Names, name) {
				perr.Names = append(perr.Names, name)
			}
			if id := creds[label].ID.String(); !slices.Contains(hitIDs, id) {
				hitIDs = append(hitIDs, id)
			}
		}
	}
	if len(perr.Fields) == 0 {
		return nil
	}
	sort.Strings(perr.Names)
	sort.Strings(hitIDs)
	kind, actor := actorOf(p)
	resourceID := ""
	if taskID != uuid.Nil {
		resourceID = taskID.String()
	}
	auditFailure(ctx, c.db, c.log, auditEntry{
		actorKind: kind, actorID: actor,
		action: "credential.plaintext_rejected", resourceKind: resourceKind, resourceID: resourceID,
		result: resultDenied, details: map[string]any{"credential_ids": hitIDs},
	})
	return perr
}

// plaintextCandidates returns the unrevoked credentials owned by userID or
// bound to taskID, and the name each is reported by: the binding's
// reference name for bound ones, the credential's own name otherwise.
func (c *Credentials) plaintextCandidates(ctx context.Context, userID, taskID uuid.UUID) ([]store.Credential, []string, error) {
	var own []store.Credential
	if err := c.db.NewSelect().Model(&own).Where("owner_user_id = ? AND revoked_at IS NULL", userID).
		Order("id").Scan(ctx); err != nil {
		return nil, nil, fmt.Errorf("plaintext check: own credentials: %w", err)
	}
	byID := map[uuid.UUID]int{}
	var creds []store.Credential
	var names []string
	for _, cr := range own {
		byID[cr.ID] = len(creds)
		creds = append(creds, cr)
		names = append(names, cr.Name)
	}
	if taskID != uuid.Nil {
		bs, err := loadBindings(ctx, c.db, taskID)
		if err != nil {
			return nil, nil, fmt.Errorf("plaintext check: task bindings: %w", err)
		}
		refs := make([]string, 0, len(bs))
		for ref := range bs {
			refs = append(refs, ref)
		}
		sort.Strings(refs)
		for _, ref := range refs {
			b := bs[ref]
			if !b.cred.RevokedAt.IsZero() {
				continue
			}
			if i, ok := byID[b.cred.ID]; ok {
				names[i] = ref
				continue
			}
			byID[b.cred.ID] = len(creds)
			creds = append(creds, b.cred)
			names = append(names, ref)
		}
	}
	return creds, names, nil
}

// currentFingerprints returns the fingerprints of the credential's current
// version, computing and storing them when they are missing or were made
// under another master key.
func (c *Credentials) currentFingerprints(ctx context.Context, f *secrets.Fingerprinter, cr store.Credential) ([]secrets.Fingerprint, error) {
	var v store.CredentialVersion
	if err := c.db.NewSelect().Model(&v).
		Where("credential_id = ? AND version = ?", cr.ID, cr.CurrentVersion).Scan(ctx); err != nil {
		return nil, fmt.Errorf("read version %d: %w", cr.CurrentVersion, err)
	}
	if fps, ok := parseFingerprints(v.Fingerprints, f.KeyID()); ok {
		return fps, nil
	}
	values, err := c.open(v)
	if err != nil {
		return nil, err
	}
	raw, err := c.fingerprints(cr.Fields, values)
	clear(values)
	if err != nil {
		return nil, err
	}
	if _, err := c.db.NewUpdate().Model((*store.CredentialVersion)(nil)).Set("fingerprints = ?", raw).
		Where("id = ?", v.ID).Exec(ctx); err != nil {
		// The fingerprints are still good for this check.
		c.log.WarnContext(ctx, "store credential fingerprints", "credential_id", cr.ID, "error", err)
	}
	fps, ok := parseFingerprints(raw, f.KeyID())
	if !ok {
		return nil, errors.New("fingerprints just computed do not parse")
	}
	return fps, nil
}
