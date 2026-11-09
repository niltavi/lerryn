package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/niltavi/lerryn/server/internal/notify"
	"github.com/niltavi/lerryn/server/internal/secrets"
)

const (
	aiTimeout          = 30 * time.Second
	maxAIResponseBytes = 1 << 20
	// Reserve room for the model and JSON envelope below the MCP result cap.
	maxAIResultBytes = 30 << 10
)

var ErrAIProvider = errors.New("AI provider call failed")

type AIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type aiProvider struct {
	guard  *notify.Guard
	client *http.Client
}

func newAIProvider(guard *notify.Guard) aiProvider {
	return aiProvider{guard: guard, client: guard.HTTPClient(aiTimeout)}
}

func (p aiProvider) chat(ctx context.Context, baseURL, key, model string, messages []AIMessage) (string, error) {
	if key == "" {
		return "", ErrAIProvider
	}
	keyForms := secrets.EncodedForms(key)
	for i, message := range messages {
		if containsAIKey(message.Content, key, keyForms) {
			var v validation
			v.content(fmt.Sprintf("#/messages/%d/content", i), "Keep the API key out of the message")
			return "", v.err()
		}
	}
	body, err := json.Marshal(struct {
		Model    string      `json:"model"`
		Messages []AIMessage `json:"messages"`
		Stream   bool        `json:"stream"`
	}{Model: model, Messages: messages})
	if err != nil {
		return "", fmt.Errorf("encode AI request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", ErrAIProvider
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		// The URL and upstream error may contain sensitive data.
		return "", ErrAIProvider
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("%w: status %d", ErrAIProvider, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAIResponseBytes+1))
	if err != nil || len(data) > maxAIResponseBytes {
		return "", ErrAIProvider
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content *string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &result); err != nil || len(result.Choices) == 0 || result.Choices[0].Message.Content == nil {
		return "", ErrAIProvider
	}
	content := *result.Choices[0].Message.Content
	encoded, err := json.Marshal(content)
	if err != nil || len(encoded) > maxAIResultBytes {
		return "", ErrAIProvider
	}
	if containsAIKey(content, key, keyForms) {
		return "", ErrAIProvider
	}
	return content, nil
}

func containsAIKey(text, key string, encodedForms []string) bool {
	if strings.Contains(text, key) {
		return true
	}
	for _, form := range encodedForms {
		if len(form) >= secrets.FingerprintMinBytes && strings.Contains(text, form) {
			return true
		}
	}
	return false
}
