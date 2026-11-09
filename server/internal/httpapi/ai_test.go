package httpapi

import (
	"net/http"
	"slices"
	"strings"
	"testing"
)

func TestAIConfigurationAPI(t *testing.T) {
	c := &m4Client{apiClient: newCredClient(t).apiClient}
	alice, _ := c.register("alice")
	bob, _ := c.register("bob")
	aliceToken := c.token(alice, "read", "write", "execute")
	bobToken := c.token(bob, "read", "execute")
	readOnlyToken := c.token(alice, "read")
	const key = "sk-api-test-secret-0123456789"
	const settings = "/api/v1/me/ai"
	const models = "/api/v1/ai/models"
	const chat = "/api/v1/ai/chat"

	body := map[string]any{"base_url": "https://provider.example/v1", "api_key": key, "models": []string{"provider/model"}}
	c.want(c.call(http.MethodPut, settings, body), http.StatusUnauthorized, ErrorCodeUnauthenticated)
	c.want(c.call(http.MethodPut, settings, body, "Authorization", aliceToken), http.StatusForbidden, ErrorCodeInsufficientScope)
	c.want(c.call(http.MethodGet, settings, nil, "Authorization", aliceToken), http.StatusForbidden, ErrorCodeInsufficientScope)
	c.want(c.call(http.MethodPut, settings, body, "Cookie", alice, "Origin", "https://other.example"), http.StatusForbidden, ErrorCodeOriginNotAllowed)
	res := c.web(alice, http.MethodPut, settings, body)
	c.want(res, http.StatusOK, "")
	if strings.Contains(res.Body.String(), key) || strings.Contains(res.Body.String(), "api_key") {
		t.Fatal("configuration response includes an API key")
	}
	res = c.web(alice, http.MethodGet, settings, nil)
	c.want(res, http.StatusOK, "")
	if strings.Contains(res.Body.String(), key) || strings.Contains(res.Body.String(), "api_key") {
		t.Fatal("configuration read includes an API key")
	}
	res = c.call(http.MethodGet, models, nil, "Authorization", readOnlyToken)
	c.want(res, http.StatusOK, "")
	if got := decode[AIModelList](t, res); !slices.Equal(got.Models, []string{"provider/model"}) {
		t.Fatalf("model list = %+v", got)
	}
	res = c.call(http.MethodGet, models, nil, "Authorization", bobToken)
	c.want(res, http.StatusOK, "")
	if got := decode[AIModelList](t, res); got.Models == nil || len(got.Models) != 0 {
		t.Fatalf("unconfigured user's model list = %+v", got)
	}

	request := map[string]any{"model": "other", "messages": []map[string]string{{"role": "user", "content": "Summarize"}}}
	c.want(c.call(http.MethodPost, chat, request, "Authorization", readOnlyToken), http.StatusForbidden, ErrorCodeInsufficientScope)
	c.want(c.call(http.MethodPost, chat, request, "Authorization", aliceToken), http.StatusUnprocessableEntity, ErrorCodeValidationFailed)
	request["model"] = "provider/model"
	c.want(c.call(http.MethodPost, chat, request, "Authorization", bobToken), http.StatusUnprocessableEntity, ErrorCodeCredentialMissing)
	c.want(c.web(alice, http.MethodDelete, settings, nil), http.StatusNoContent, "")
	c.want(c.call(http.MethodPost, chat, request, "Authorization", aliceToken), http.StatusUnprocessableEntity, ErrorCodeCredentialMissing)
}
