package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/notify"
	"github.com/niltavi/lerryn/server/internal/secrets"
	"github.com/niltavi/lerryn/server/internal/store"
)

const (
	// The name is outside the user credential name grammar. Keeping the
	// configuration in the vault gives it the same encryption and key rotation.
	aiCredentialName = "@ai"
	aiKeyField       = "API_KEY"
	aiBaseURLField   = "BASE_URL"
	aiModelsField    = "MODELS"
)

type AIConfiguration struct {
	BaseURL string
	Models  []string
}

func (c *Credentials) WithAINetworkGuard(guard *notify.Guard) *Credentials {
	c.ai = newAIProvider(guard)
	return c
}

func (c *Credentials) AIConfiguration(ctx context.Context, p *auth.Principal) (AIConfiguration, error) {
	if p == nil {
		return AIConfiguration{}, ErrUnauthenticated
	}
	if p.Method != auth.MethodSession {
		return AIConfiguration{}, ErrForbidden
	}
	return c.readAIConfiguration(ctx, p)
}

func (c *Credentials) AIModels(ctx context.Context, p *auth.Principal) ([]string, error) {
	config, err := c.readAIConfiguration(ctx, p)
	return config.Models, err
}

func (c *Credentials) readAIConfiguration(ctx context.Context, p *auth.Principal) (AIConfiguration, error) {
	if p == nil {
		return AIConfiguration{}, ErrUnauthenticated
	}
	if !p.HasScope(auth.ScopeRead) {
		return AIConfiguration{}, ErrForbidden
	}
	values, err := c.aiValues(ctx, c.db, p)
	if err != nil {
		return AIConfiguration{}, err
	}
	defer clear(values)
	return aiConfiguration(values), nil
}

func aiConfiguration(values map[string]string) AIConfiguration {
	if len(values) == 0 {
		return AIConfiguration{Models: []string{}}
	}
	return AIConfiguration{BaseURL: values[aiBaseURLField], Models: strings.Split(values[aiModelsField], "\n")}
}

func (c *Credentials) aiValues(ctx context.Context, db bun.IDB, p *auth.Principal) (map[string]string, error) {
	// Read the active configuration and its version in one statement so a
	// concurrent change cannot mix a previous key with a new address.
	var version store.CredentialVersion
	err := db.NewSelect().Model(&version).
		Join("JOIN credentials AS c ON c.id = credential_version.credential_id AND c.current_version = credential_version.version").
		Where("c.owner_user_id = ? AND c.name = ?", p.UserID, aiCredentialName).
		Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read AI configuration: %w", err)
	}
	return c.open(version)
}

func (c *Credentials) SetAIConfiguration(ctx context.Context, p *auth.Principal, config AIConfiguration, key string) (AIConfiguration, error) {
	if err := requireAIConfigWrite(p); err != nil {
		return AIConfiguration{}, err
	}
	config.BaseURL = strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if err := c.validateAIConfiguration(config, key); err != nil {
		return AIConfiguration{}, err
	}
	texts := []ScanText{{Base: "#/base_url", Text: config.BaseURL}}
	for i, model := range config.Models {
		texts = append(texts, ScanText{Base: "#/models", Path: []string{strconv.Itoa(i)}, Text: model})
	}
	if err := c.checkAIPlaintext(ctx, p, texts); err != nil {
		return AIConfiguration{}, err
	}
	err := c.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		if err := lockAIConfiguration(ctx, tx, p); err != nil {
			return err
		}
		var cred store.Credential
		err := tx.NewSelect().Model(&cred).
			Where("owner_user_id = ? AND name = ?", p.UserID, aiCredentialName).Scan(ctx)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		current, err := c.aiValues(ctx, tx, p)
		if err != nil && key == "" {
			return err
		}
		// A replacement key can recover a configuration whose old key is unreadable.
		defer clear(current)
		currentKey := current[aiKeyField]
		if key == "" {
			if current[aiBaseURLField] != config.BaseURL {
				var v validation
				v.content("#/api_key", "Enter an API key when setting or changing the base URL")
				return v.err()
			}
			key = currentKey
		}
		models := strings.Join(config.Models, "\n")
		for _, candidate := range []string{key, currentKey} {
			if candidate == "" {
				continue
			}
			keyForms := secrets.EncodedForms(candidate)
			if containsAIKey(config.BaseURL, candidate, keyForms) || containsAIKey(models, candidate, keyForms) {
				var v validation
				v.content("#", "Keep the API key out of the base URL and model IDs")
				return v.err()
			}
		}
		values := map[string]string{aiKeyField: key, aiBaseURLField: config.BaseURL, aiModelsField: models}
		defer clear(values)
		now := c.now()
		if cred.CurrentVersion == 0 {
			cred = store.Credential{
				ID: store.NewID(), OwnerUserID: p.UserID, Name: aiCredentialName, Purpose: "AI",
				Fields: []store.CredentialField{
					{Name: aiKeyField, Secret: true},
					{Name: aiBaseURLField},
					{Name: aiModelsField},
				},
				Status: store.CredentialActive, CurrentVersion: 1, CreatedAt: now, UpdatedAt: now,
			}
			if _, err := tx.NewInsert().Model(&cred).Exec(ctx); err != nil {
				return err
			}
		} else {
			cred.CurrentVersion++
			if _, err := tx.NewUpdate().Model(&cred).
				Set("current_version = ?", cred.CurrentVersion).Set("updated_at = ?", now).
				WherePK().Exec(ctx); err != nil {
				return err
			}
		}
		if err := c.insertVersion(ctx, tx, p, cred, values); err != nil {
			return err
		}
		return auditAIConfiguration(ctx, tx, p, "ai.configure", cred.ID.String())
	})
	if err != nil {
		return AIConfiguration{}, fmt.Errorf("save AI configuration: %w", err)
	}
	return config, nil
}

func (c *Credentials) validateAIConfiguration(config AIConfiguration, key string) error {
	var v validation
	u, err := url.Parse(config.BaseURL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.ForceQuery ||
		u.RawQuery != "" || strings.Contains(config.BaseURL, "#") ||
		len(config.BaseURL) > 2048 || c.ai.guard.CheckURL(config.BaseURL) != nil ||
		strings.HasSuffix(u.Path, "/chat/completions") {
		v.content("#/base_url", "Enter a permitted HTTPS API base URL without credentials, query, fragment, or the chat completions path")
	}
	if len(key) > 4096 || strings.ContainsFunc(key, func(r rune) bool { return r < '!' || r > '~' }) {
		v.content("#/api_key", "Enter an API key without spaces or control characters")
	}
	if len(config.Models) == 0 || len(config.Models) > 50 {
		v.content("#/models", "Enter between 1 and 50 model IDs")
	}
	seen := make(map[string]bool, len(config.Models))
	for i, model := range config.Models {
		if model == "" || len(model) > 200 || seen[model] ||
			strings.ContainsFunc(model, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
			v.content(fmt.Sprintf("#/models/%d", i), "Enter a unique model ID without spaces or control characters")
		}
		seen[model] = true
	}
	return v.err()
}

func (c *Credentials) RemoveAIConfiguration(ctx context.Context, p *auth.Principal) error {
	if err := requireAIConfigWrite(p); err != nil {
		return err
	}
	return c.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		if err := lockAIConfiguration(ctx, tx, p); err != nil {
			return err
		}
		var cred store.Credential
		err := tx.NewDelete().Model(&cred).Where("owner_user_id = ? AND name = ?", p.UserID, aiCredentialName).
			Returning("id").Scan(ctx)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("remove AI configuration: %w", err)
		}
		return auditAIConfiguration(ctx, tx, p, "ai.remove", cred.ID.String())
	})
}

func requireAIConfigWrite(p *auth.Principal) error {
	switch {
	case p == nil:
		return ErrUnauthenticated
	case p.Method != auth.MethodSession || p.ReadOnly || !p.HasScope(auth.ScopeWrite):
		return ErrForbidden
	default:
		return nil
	}
}

func lockAIConfiguration(ctx context.Context, tx bun.Tx, p *auth.Principal) error {
	// There is no credential row to lock on the first save.
	_, err := tx.NewUpdate().Model((*store.User)(nil)).Set("updated_at = updated_at").
		Where("id = ?", p.UserID).Exec(ctx)
	return err
}

func auditAIConfiguration(ctx context.Context, tx bun.Tx, p *auth.Principal, action, id string) error {
	kind, actor := actorOf(p)
	return writeAudit(ctx, tx, auditEntry{
		actorKind: kind, actorID: actor, action: action, resourceKind: "credential", resourceID: id,
	})
}

var ErrAINotConfigured = errors.New("AI is not configured")

func (c *Credentials) AIChat(ctx context.Context, p *auth.Principal, model string, messages []AIMessage) (string, error) {
	if p == nil {
		return "", ErrUnauthenticated
	}
	if p.ReadOnly || !p.HasScope(auth.ScopeExecute) {
		return "", ErrForbidden
	}
	values, err := c.aiValues(ctx, c.db, p)
	if err != nil {
		return "", err
	}
	defer clear(values)
	if len(values) == 0 {
		return "", ErrAINotConfigured
	}
	config := aiConfiguration(values)
	if !slices.Contains(config.Models, model) {
		var v validation
		v.content("#/model", "Choose a model enabled in AI settings")
		return "", v.err()
	}
	texts := make([]ScanText, len(messages))
	for i, message := range messages {
		texts[i] = ScanText{Base: "#/messages", Path: []string{strconv.Itoa(i), "content"}, Text: message.Content}
	}
	if err := c.checkAIPlaintext(ctx, p, texts); err != nil {
		return "", err
	}
	content, err := c.ai.chat(ctx, config.BaseURL, values[aiKeyField], model, messages)
	if err != nil {
		return "", err
	}
	if err := c.checkAIPlaintext(ctx, p, []ScanText{{Base: "#/content", Text: content}}); err != nil {
		if _, ok := errors.AsType[*ValidationError](err); ok {
			return "", ErrAIProvider
		}
		return "", err
	}
	return content, nil
}

func (c *Credentials) checkAIPlaintext(ctx context.Context, p *auth.Principal, texts []ScanText) error {
	err := c.checkPlaintext(ctx, p, uuid.Nil, texts, "ai")
	if detected, ok := errors.AsType[*PlaintextCredentialError](err); ok {
		return &detected.ValidationError
	}
	return err
}
