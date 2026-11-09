package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/service"
)

func (h *handler) GetAIConfiguration(ctx context.Context, _ GetAIConfigurationRequestObject) (GetAIConfigurationResponseObject, error) {
	if h.credentials == nil {
		return nil, errNoCredentials
	}
	cfg, err := h.credentials.AIConfiguration(ctx, auth.PrincipalFrom(ctx))
	if err != nil {
		return errorResponse[GetAIConfigurationdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return GetAIConfiguration200JSONResponse{BaseURL: credString(cfg.BaseURL), Models: cfg.Models}, nil
}

func (h *handler) SetAIConfiguration(ctx context.Context, req SetAIConfigurationRequestObject) (SetAIConfigurationResponseObject, error) {
	if h.credentials == nil {
		return nil, errNoCredentials
	}
	cfg, err := h.credentials.SetAIConfiguration(ctx, auth.PrincipalFrom(ctx), service.AIConfiguration{
		BaseURL: req.Body.BaseURL,
		Models:  req.Body.Models,
	}, credValue(req.Body.APIKey))
	if err != nil {
		return errorResponse[SetAIConfigurationdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return SetAIConfiguration200JSONResponse{BaseURL: credString(cfg.BaseURL), Models: cfg.Models}, nil
}

func (h *handler) RemoveAIConfiguration(ctx context.Context, _ RemoveAIConfigurationRequestObject) (RemoveAIConfigurationResponseObject, error) {
	if h.credentials == nil {
		return nil, errNoCredentials
	}
	if err := h.credentials.RemoveAIConfiguration(ctx, auth.PrincipalFrom(ctx)); err != nil {
		return errorResponse[RemoveAIConfigurationdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return RemoveAIConfiguration204Response{}, nil
}

func (h *handler) ListAIModels(ctx context.Context, _ ListAIModelsRequestObject) (ListAIModelsResponseObject, error) {
	if h.credentials == nil {
		return nil, errNoCredentials
	}
	models, err := h.credentials.AIModels(ctx, auth.PrincipalFrom(ctx))
	if err != nil {
		return errorResponse[ListAIModelsdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return ListAIModels200JSONResponse{Models: models}, nil
}

func (h *handler) CreateAIChat(ctx context.Context, req CreateAIChatRequestObject) (CreateAIChatResponseObject, error) {
	if h.credentials == nil {
		return nil, errNoCredentials
	}
	messages := make([]service.AIMessage, len(req.Body.Messages))
	for i, msg := range req.Body.Messages {
		messages[i] = service.AIMessage{Role: string(msg.Role), Content: msg.Content}
	}
	content, err := h.credentials.AIChat(ctx, auth.PrincipalFrom(ctx), req.Body.Model, messages)
	switch {
	case errors.Is(err, service.ErrAINotConfigured):
		p := buildProblem(ctx, http.StatusUnprocessableEntity, ErrorCodeCredentialMissing, "当前用户尚未配置 AI 能力")
		p.NextActions = &[]NextAction{{Action: "configure_ai", Hint: "请用户在 Web 控制台的凭证页配置 AI 能力"}}
		return CreateAIChatdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
	case errors.Is(err, service.ErrAIProvider):
		return problemResponse[CreateAIChatdefaultApplicationProblemPlusJSONResponse](ctx, http.StatusBadGateway,
			ErrorCodeAIProviderError, "请检查 AI 服务地址、API Key、模型及服务商状态"), nil
	case err != nil:
		return errorResponse[CreateAIChatdefaultApplicationProblemPlusJSONResponse](ctx, err)
	default:
		return CreateAIChat200JSONResponse{Model: req.Body.Model, Content: content}, nil
	}
}
