package store_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/store"
)

// roundTrip inserts model, reads it back by primary key into a new value
// and checks every field survived. Times are compared in UTC: pgx returns
// them in the local zone.
func roundTrip(t *testing.T, q *queue, model any) {
	t.Helper()
	ctx := context.Background()
	if _, err := q.db.NewInsert().Model(model).Exec(ctx); err != nil {
		t.Fatalf("insert %T: %v", model, err)
	}
	got := reflect.New(reflect.TypeOf(model).Elem()).Interface()
	id := reflect.ValueOf(model).Elem().FieldByName("ID").Interface()
	if err := q.db.NewSelect().Model(got).Where("id = ?", id).Scan(ctx); err != nil {
		t.Fatalf("select %T: %v", model, err)
	}
	utc(got)
	want, _ := json.Marshal(model)
	have, _ := json.Marshal(got)
	if string(want) != string(have) {
		t.Fatalf("%T round trip:\ngot  %s\nwant %s", model, have, want)
	}
}

// utc converts the time.Time fields of the struct ptr points to to UTC.
func utc(ptr any) {
	v := reflect.ValueOf(ptr).Elem()
	for i := range v.NumField() {
		if f := v.Field(i); f.CanSet() && f.Type() == reflect.TypeFor[time.Time]() {
			f.Set(reflect.ValueOf(f.Interface().(time.Time).UTC()))
		}
	}
}

// Every table added for M2 to M6 accepts a fully populated model and
// returns it unchanged, on both databases.
func TestContractModelsRoundTrip(t *testing.T) {
	t.Parallel()
	q := newQueue(t)
	ctx := context.Background()
	now := store.Now()
	later := now.Add(time.Hour)
	run := q.mustInsertRun()

	trigger := &store.Trigger{ // #nosec G101 -- test fixture; TokenHash is not a real token hash
		ID: store.NewID(), TaskID: q.task.ID, Name: "weekday-morning", Type: store.TriggerTypeWebhook, Enabled: true,
		Schedule: "0 8 * * 1-5", Timezone: "Asia/Shanghai", CatchUp: store.CatchUpLatest, Params: `{"a":1}`,
		MaxBodyBytes: 1 << 20, TokenHash: "hash", TokenPrefix: "wl_hook_ab", NextFireAt: later, LastFiredAt: now,
		CreatedBy: q.user.ID, CreatedAt: now, UpdatedAt: now,
	}
	session := &store.Session{
		ID: store.NewID(), UserID: q.user.ID, TokenHash: "session-hash", Kind: store.SessionMobile,
		CreatedAt: now, ExpiresAt: later, LastSeenAt: now,
		DeviceName: "张林的 iPhone", DevicePlatform: "ios", DeviceOSVersion: "iOS 26.1",
	}
	credential := &store.Credential{
		ID: store.NewID(), OwnerUserID: q.user.ID, Name: "LLM_API_KEY", Purpose: "调用模型",
		Fields: []store.CredentialField{{Name: "LLM_API_KEY", Label: "Key", Secret: true, Multiline: true, Optional: true}},
		Status: store.CredentialActive, CurrentVersion: 1, ExpiresAt: later, LastUsedAt: now, ExpiryNotifiedAt: now,
		CreatedAt: now, UpdatedAt: now,
	}
	channel := &store.NotificationChannel{
		ID: store.NewID(), OwnerUserID: q.user.ID, Name: "值班群", Kind: store.ChannelFeishu, Enabled: true,
		URLPreview: "https://open.feishu.cn/…/a91c", SecretCiphertext: []byte{0, 1, 2, 255}, SecretWrappedDEK: []byte{9, 8},
		SecretKEKID: "local-1", CreatedAt: now, UpdatedAt: now,
	}
	models := []any{
		trigger,
		&store.TriggerFire{ID: store.NewID(), TriggerID: trigger.ID, ScheduledAt: now, Outcome: store.FireSkipped, RunID: run.ID, MissedCount: 3, CreatedAt: now},
		&store.IdempotencyRecord{ID: store.NewID(), Principal: "user:" + q.user.ID.String(), Key: "k1", Operation: "startRun", RequestHash: "rh", Status: 202, Response: `{"id":"x"}`, CreatedAt: now, ExpiresAt: later},
		&store.Identity{ID: store.NewID(), UserID: q.user.ID, Issuer: "https://sso.example.com", Subject: "s-1", Username: "zhanglin", Email: "z@example.com", CreatedAt: now, LastLoginAt: now},
		session,
		&store.PairingCode{ID: store.NewID(), UserID: q.user.ID, CodeHash: "code-hash", CreatedAt: now, ExpiresAt: later, UsedAt: now, SessionID: session.ID},
		&store.Setting{ID: store.NewID(), Key: store.SettingOpenRegistration, Value: "false", UpdatedBy: q.user.ID, UpdatedAt: now},
		credential,
		&store.CredentialVersion{ID: store.NewID(), CredentialID: credential.ID, Version: 1, Ciphertext: []byte{1, 2, 3}, WrappedDEK: []byte{4, 5}, KEKID: "local-1", CreatedBy: q.user.ID, CreatedAt: now},
		&store.CredentialGrant{ID: store.NewID(), CredentialID: credential.ID, TaskID: q.task.ID, Name: "LLM_API_KEY", GrantedBy: q.user.ID, CreatedAt: now},
		&store.CredentialRequest{
			ID: store.NewID(), UserID: q.user.ID, TaskID: q.task.ID, CredentialID: credential.ID, Name: "LLM_API_KEY", Purpose: "调用模型",
			Fields: credential.Fields, Status: store.CredentialRequestFulfilled, Client: "Claude Code", CreatedAt: now, ExpiresAt: later,
			FulfilledAt: now, CancelledAt: now,
		},
		&store.Artifact{ID: store.NewID(), RunID: run.ID, Path: "out/digest.md", Title: "日报", Size: 4200, ContentType: "text/markdown", SHA256: "sha", BlobKey: "artifacts/x", CreatedAt: now, ExpiredAt: later},
		channel,
		&store.NotificationSubscription{ID: store.NewID(), ChannelID: channel.ID, ProjectID: q.project.ID, TaskID: q.task.ID, Events: []string{"run_failed", "run_timed_out"}, CreatedAt: now},
		&store.NotificationDelivery{
			ID: store.NewID(), ChannelID: channel.ID, EventType: "run_failed", MergeKey: "task:x", TaskID: q.task.ID, RunID: run.ID, MergedCount: 2,
			Payload: `{"type":"run_failed"}`, Status: store.DeliveryFailed, Attempts: 3, HTTPStatus: 502, Error: "bad gateway",
			NextAttemptAt: later, LastAttemptAt: now, DeliveredAt: now, CreatedAt: now, ScheduledAt: now,
		},
		&store.NotificationEventRecord{ID: store.NewID(), EventType: "run_failed", RunID: run.ID, Deliveries: 2, CreatedAt: now},
		&store.NotificationEventRecord{
			ID: store.NewID(), EventType: "credential_expiring", CredentialID: credential.ID, CredentialExpiresAt: later, CreatedAt: now,
		},
		&store.BlobRelease{ID: store.NewID(), Hash: "ab12", ReleasedAt: now, DeletedAt: later},
	}
	for _, m := range models {
		roundTrip(t, q, m)
	}

	// The columns added to existing tables.
	run.TriggerID, run.TriggerFireID, run.ScheduledAt = trigger.ID, store.NewID(), now
	run.Client, run.Attempt, run.MaxAttempts, run.RetryOf, run.NotBefore = "Codex", 2, 3, run.ID, later
	run.RerunOf, run.SkipReason, run.ClaimedAt, run.RunnerName = run.ID, store.SkipOverlap, now, "runner-01"
	run.CancelRequestedBy, run.CancelClient, run.InputKey, run.InputContentType = q.user.ID, "Codex", "inputs/x", "application/json"
	logBytes := int64(4096)
	run.LogBlob, run.LogBytes, run.LogsExpiredAt = "cd34", &logBytes, later
	if _, err := q.db.NewUpdate().Model(&run).WherePK().Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if got := q.get(run.ID); got.Attempt != 2 || got.RerunOf != run.ID || got.SkipReason != store.SkipOverlap || !got.ClaimedAt.Equal(now) || got.InputContentType != "application/json" ||
		got.LogBlob != "cd34" || got.LogBytes == nil || *got.LogBytes != 4096 || !got.LogsExpiredAt.Equal(later) {
		t.Fatalf("run = %+v", got)
	}
}

// Models that leave defaulted columns zero get the column defaults, so
// code written before a column existed keeps inserting valid rows.
func TestContractColumnDefaults(t *testing.T) {
	t.Parallel()
	q := newQueue(t)
	ctx := context.Background()

	var task store.Task
	if err := q.db.NewSelect().Model(&task).Where("id = ?", q.task.ID).Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if task.State != store.TaskInactive || task.ActiveSeq != 0 || task.TriggersRevision != 0 || !task.ArchivedAt.IsZero() {
		t.Fatalf("task = %+v", task)
	}
	if run := q.get(q.mustInsertRun().ID); run.Attempt != 1 || run.MaxAttempts != 1 {
		t.Fatalf("attempt = %d, max_attempts = %d, want 1 and 1", run.Attempt, run.MaxAttempts)
	}

	now := store.Now()
	trigger := store.Trigger{ID: store.NewID(), TaskID: q.task.ID, Name: "t", Type: store.TriggerTypeCron, CreatedBy: q.user.ID, CreatedAt: now, UpdatedAt: now}
	if _, err := q.db.NewInsert().Model(&trigger).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := q.db.NewSelect().Model(&trigger).WherePK().Scan(ctx); err != nil || trigger.CatchUp != store.CatchUpSkip {
		t.Fatalf("catch_up = %q, err = %v", trigger.CatchUp, err)
	}
}

// Archiving frees a task name; two live tasks still cannot share one.
func TestArchivedTaskFreesName(t *testing.T) {
	t.Parallel()
	q := newQueue(t)
	ctx := context.Background()
	now := store.Now()
	newTask := func() store.Task {
		return store.Task{ID: store.NewID(), ProjectID: q.project.ID, Name: q.task.Name, LatestSeq: 1, CreatedBy: q.user.ID, CreatedAt: now, UpdatedAt: now}
	}

	dup := newTask()
	if _, err := q.db.NewInsert().Model(&dup).Exec(ctx); !store.IsUniqueViolation(err) {
		t.Fatalf("second live task with the same name: err = %v, want a unique violation", err)
	}
	if _, err := q.db.NewUpdate().Model((*store.Task)(nil)).Set("archived_at = ?", now).Where("id = ?", q.task.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	again := newTask()
	if _, err := q.db.NewInsert().Model(&again).Exec(ctx); err != nil {
		t.Fatalf("task with the name of an archived one: %v", err)
	}
}

// A credential name is unique per owner among unrevoked credentials, and a
// task binds at most one credential per reference name.
func TestCredentialUniqueness(t *testing.T) {
	t.Parallel()
	q := newQueue(t)
	ctx := context.Background()
	now := store.Now()
	cred := func() store.Credential {
		return store.Credential{ID: store.NewID(), OwnerUserID: q.user.ID, Name: "TOKEN", Purpose: "p", Fields: []store.CredentialField{{Name: "TOKEN", Secret: true}}, CurrentVersion: 1, CreatedAt: now, UpdatedAt: now}
	}
	first := cred()
	if _, err := q.db.NewInsert().Model(&first).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	second := cred()
	if _, err := q.db.NewInsert().Model(&second).Exec(ctx); !store.IsUniqueViolation(err) {
		t.Fatalf("two live credentials named TOKEN: err = %v", err)
	}
	first.RevokedAt, first.Status = now, store.CredentialRevoked
	if _, err := q.db.NewUpdate().Model(&first).WherePK().Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := q.db.NewInsert().Model(&second).Exec(ctx); err != nil {
		t.Fatalf("new credential after revoking the old one: %v", err)
	}

	grant := func(credID uuid.UUID) store.CredentialGrant {
		return store.CredentialGrant{ID: store.NewID(), CredentialID: credID, TaskID: q.task.ID, Name: "TOKEN", GrantedBy: q.user.ID, CreatedAt: now}
	}
	g1 := grant(first.ID)
	if _, err := q.db.NewInsert().Model(&g1).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	g2 := grant(second.ID)
	if _, err := q.db.NewInsert().Model(&g2).Exec(ctx); !store.IsUniqueViolation(err) {
		t.Fatalf("two credentials bound to one reference name: err = %v", err)
	}
}
