package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The generic tools reach every operation of the contract, including those
// without a dedicated tool (tech design 10.2). They are part of every
// server, so no operation may claim their names.
const (
	describeToolName = "api_describe"
	callToolName     = "api_call"
)

var describeToolDef = &mcpsdk.Tool{
	Name:  describeToolName,
	Title: "查看接口说明",
	Description: "查看 Lerryn HTTP API 的接口。传 operation_id 返回这个接口的说明、所需令牌范围、输入 schema 和响应 schema；" +
		"传 query 按关键词搜索接口；都不传时列出全部接口。没有独立工具的接口用 api_call 调用，参数按这里的 input_schema 填写。",
	InputSchema: map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"operation_id": map[string]any{
				"type":        "string",
				"description": "接口的 operationId，例如 getTask。",
			},
			"query": map[string]any{
				"type":        "string",
				"description": "关键词，匹配 operationId、路径、摘要、说明和标签。多个词用空格分隔，需全部匹配。",
			},
		},
	},
	Annotations: &mcpsdk.ToolAnnotations{
		ReadOnlyHint:    true,
		DestructiveHint: new(false),
		IdempotentHint:  true,
		OpenWorldHint:   new(false),
	},
}

// api_call can reach operations of every kind, so its annotations are the
// most cautious ones; the token scope still limits what a call can do.
var callToolDef = &mcpsdk.Tool{
	Name:  callToolName,
	Title: "调用接口",
	Description: "按 operationId 调用 Lerryn HTTP API 的任意接口，与独立工具走同一条处理链，同样检查令牌范围。" +
		"先用 api_describe 查看接口的 input_schema，arguments 按它填写：路径、查询和请求头参数用 snake_case 名称，请求体的属性平铺在同一层。" +
		"有独立工具的接口优先用独立工具。",
	InputSchema: map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"operation_id"},
		"properties": map[string]any{
			"operation_id": map[string]any{
				"type":        "string",
				"description": "接口的 operationId，用 api_describe 查找。",
			},
			"arguments": map[string]any{
				"type":        "object",
				"description": "接口的参数，结构见 api_describe 返回的 input_schema。",
			},
		},
	},
	Annotations: &mcpsdk.ToolAnnotations{
		DestructiveHint: new(true),
		IdempotentHint:  false,
		OpenWorldHint:   new(true),
	},
}

// OperationSummary is one entry of an operation listing.
type OperationSummary struct {
	OperationID string        `json:"operation_id"`
	Method      string        `json:"method"`
	Path        string        `json:"path"`
	Summary     string        `json:"summary"`
	Kind        OperationKind `json:"kind"`
	Tool        *string       `json:"tool"`
}

// OperationDetail is what api_describe returns for one operation.
type OperationDetail struct {
	OperationSummary
	Description   string          `json:"description"`
	Tags          []string        `json:"tags"`
	Deprecated    bool            `json:"deprecated"`
	RequiredScope string          `json:"required_scope"`
	Anonymous     bool            `json:"anonymous"`
	SessionOnly   bool            `json:"session_only"`
	Callable      bool            `json:"callable"`
	NotCallable   string          `json:"not_callable_reason,omitempty"`
	InputSchema   map[string]any  `json:"input_schema"`
	Response      *ResponseDetail `json:"response"`
}

// ResponseDetail describes the success response of an operation.
type ResponseDetail struct {
	Status    int            `json:"status"`
	MediaType string         `json:"media_type,omitempty"`
	Schema    map[string]any `json:"schema,omitempty"`
}

// Summarize returns the listing entry of the operation.
func (o *Operation) Summarize() OperationSummary {
	s := OperationSummary{OperationID: o.OperationID, Method: o.Method, Path: o.Path, Summary: o.Summary, Kind: o.Kind}
	if o.ToolName != "" {
		s.Tool = new(o.ToolName)
	}
	return s
}

// NotCallableReason explains why api_call cannot call the operation with an
// access token, or returns "" when it can.
func (o *Operation) NotCallableReason() string {
	switch {
	case o.Unsupported != "":
		return "参数无法映射为 JSON 请求，请直接使用 HTTP API：" + o.Unsupported
	case o.Streaming:
		return "响应是事件流，无法作为一次工具结果返回，请直接使用 HTTP API"
	case o.SessionOnly:
		return "只能在控制台的登录会话中调用，访问令牌无法调用"
	}
	return ""
}

// Detail returns the full description of the operation.
func (o *Operation) Detail() OperationDetail {
	d := OperationDetail{
		OperationSummary: o.Summarize(),
		Description:      o.Description,
		Tags:             o.Tags,
		Deprecated:       o.Deprecated,
		RequiredScope:    o.Scope(),
		Anonymous:        o.Anonymous,
		SessionOnly:      o.SessionOnly,
		NotCallable:      o.NotCallableReason(),
		InputSchema:      o.InputSchema,
	}
	d.Callable = d.NotCallable == ""
	if d.Tags == nil {
		d.Tags = []string{}
	}
	if o.SuccessStatus != 0 {
		d.Response = &ResponseDetail{Status: o.SuccessStatus, MediaType: o.ResponseMediaType, Schema: o.ResponseSchema}
	}
	return d
}

// Search returns the operations matching every word of query, case
// insensitively, in operationId order. An empty query matches all.
func (r *Registry) Search(query string) []*Operation {
	words := strings.Fields(strings.ToLower(query))
	var out []*Operation
	for _, o := range r.ops {
		hay := strings.ToLower(strings.Join(append([]string{o.OperationID, o.ToolName, o.Method, o.Path, o.Summary, o.Description}, o.Tags...), "\n"))
		match := true
		for _, w := range words {
			if !strings.Contains(hay, w) {
				match = false
				break
			}
		}
		if match {
			out = append(out, o)
		}
	}
	return out
}

func decodeStrict(raw json.RawMessage, v any) *ArgError {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return &ArgError{msg: fmt.Sprintf("arguments: %v", err)}
	}
	return nil
}

func jsonResult(v any) (*mcpsdk.CallToolResult, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode result: %w", err)
	}
	return &mcpsdk.CallToolResult{Content: textContent(string(data)), StructuredContent: json.RawMessage(data)}, nil
}

var describeAgain = nextAction{action: describeToolName, hint: "用 query 搜索接口，确认 operation_id"}

func describeHandler(reg *Registry) mcpsdk.ToolHandler {
	return func(_ context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		var in struct {
			OperationID string `json:"operation_id"`
			Query       string `json:"query"`
		}
		if ae := decodeStrict(req.Params.Arguments, &in); ae != nil {
			return argErrorResult(nextAction{action: describeToolName, hint: "只传 operation_id 或 query"}, ae), nil
		}
		if in.OperationID != "" {
			o, ok := reg.Operation(in.OperationID)
			if !ok {
				return problemResult(http.StatusNotFound, "not_found", "接口不存在",
					fmt.Sprintf("没有 operationId 为 %s 的接口", in.OperationID), describeAgain), nil
			}
			return jsonResult(o.Detail())
		}
		ops := reg.Search(in.Query)
		list := make([]OperationSummary, 0, len(ops))
		for _, o := range ops {
			list = append(list, o.Summarize())
		}
		return jsonResult(map[string]any{"operations": list, "count": len(list)})
	}
}

func callHandler(reg *Registry, api http.Handler, maxBytes int, log *slog.Logger) mcpsdk.ToolHandler {
	return func(ctx context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		var in struct {
			OperationID string          `json:"operation_id"`
			Arguments   json.RawMessage `json:"arguments"`
		}
		if ae := decodeStrict(req.Params.Arguments, &in); ae != nil {
			return argErrorResult(nextAction{action: callToolName, hint: "传 operation_id 和 arguments 两个字段"}, ae), nil
		}
		if in.OperationID == "" {
			return argErrorResult(describeAgain, &ArgError{msg: "missing required argument \"operation_id\""}), nil
		}
		o, ok := reg.Operation(in.OperationID)
		if !ok {
			return problemResult(http.StatusNotFound, "not_found", "接口不存在",
				fmt.Sprintf("没有 operationId 为 %s 的接口", in.OperationID), describeAgain), nil
		}
		if o.Unsupported != "" || o.Streaming {
			return problemResult(http.StatusBadRequest, "validation_failed", "接口无法经 MCP 调用",
				o.NotCallableReason(), nextAction{action: describeToolName, hint: "查看接口说明，改用 HTTP API 或相应的独立工具"}), nil
		}
		var inbound http.Header
		if req.Extra != nil {
			inbound = req.Extra.Header
		}
		start := time.Now()
		res, err := o.call(ctx, api, callToolName, in.Arguments, inbound, maxBytes, nextAction{
			action: describeToolName,
			hint:   "查看 " + o.OperationID + " 的 input_schema，修正 arguments 后重新调用 api_call",
		})
		if err != nil {
			logCallFailure(ctx, log, err, "tool", callToolName, "operation_id", o.OperationID)
			return nil, err
		}
		if res.Meta == nil {
			res.Meta = mcpsdk.Meta{}
		}
		res.Meta[metaOperationID] = o.OperationID
		log.InfoContext(ctx, "mcp tool call",
			"tool", callToolName,
			"operation_id", o.OperationID,
			"http_status", res.Meta[metaHTTPStatus],
			"is_error", res.IsError,
			"truncated", res.Meta[metaTruncated] == true,
			"duration", time.Since(start))
		return res, nil
	}
}
