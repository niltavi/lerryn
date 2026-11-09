package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/niltavi/lerryn/server/internal/logging"
)

// problemTitles holds the fixed, human-readable title of each error code.
// Titles are user-facing text and therefore Chinese, matching section 9.4
// of the tech design. TestEveryErrorCodeHasATitle checks this table against
// the codes in the bundled spec.
var problemTitles = map[ErrorCode]string{ // #nosec G101 -- error titles; "credential" in the keys trips the heuristic
	ErrorCodeValidationFailed:        "参数校验失败",
	ErrorCodeVersionConflict:         "版本冲突",
	ErrorCodeIdempotencyConflict:     "幂等键冲突",
	ErrorCodeCredentialMissing:       "缺少凭证",
	ErrorCodeCredentialUnavailable:   "凭证不可用",
	ErrorCodeDependencyInstallFailed: "依赖安装失败",
	ErrorCodeRunnerUnavailable:       "没有可用的 runner",
	ErrorCodeQueueFull:               "队列已满",
	ErrorCodeScriptError:             "脚本执行失败",
	ErrorCodeTimeout:                 "执行超时",
	ErrorCodeRegistrationDisabled:    "注册已关闭",
	ErrorCodeInsufficientScope:       "令牌范围不足",
	ErrorCodeNotFound:                "资源不存在",
	ErrorCodeUnauthenticated:         "未认证",
	ErrorCodeUsernameTaken:           "用户名已被占用",
	ErrorCodeRateLimited:             "请求过于频繁",
	ErrorCodeOriginNotAllowed:        "请求来源不被允许",
	ErrorCodeRunInProgress:           "任务已有运行未结束",
	ErrorCodeOutOfMemory:             "内存超出上限",
	ErrorCodeRunInterrupted:          "运行被中断",
	ErrorCodeInternalError:           "服务端内部错误",
	ErrorCodePermissionDenied:        "权限不足",
	ErrorCodePreconditionRequired:    "缺少版本号",
	ErrorCodeVersionNotTested:        "版本尚未测试",
	ErrorCodeTaskArchived:            "任务已归档",
	ErrorCodePasswordLoginDisabled:   "密码登录已关闭",
	ErrorCodeLastOwner:               "不能移除最后一个所有者",
	ErrorCodeRequestClosed:           "填写请求已结束",
	ErrorCodeExpired:                 "已按保留策略清理",
	ErrorCodeManualRunDisabled:       "手动执行已关闭",
	ErrorCodeAIProviderError:         "AI 服务调用失败",
}

// buildProblem builds the problem body for code. detail may be empty.
func buildProblem(ctx context.Context, status int, code ErrorCode, detail string) Problem {
	p := Problem{
		Type:      "urn:lerryn:error:" + string(code),
		Title:     problemTitles[code],
		Status:    status,
		Code:      code,
		RequestID: RequestIDFromContext(ctx),
	}
	if detail != "" {
		p.Detail = &detail
	}
	return p
}

// newProblem is buildProblem for code that has the request at hand; it
// also records the path in instance.
func newProblem(r *http.Request, status int, code ErrorCode, detail string) Problem {
	p := buildProblem(r.Context(), status, code, detail)
	p.Instance = new(r.URL.Path)
	return p
}

// defaultResponse is the shape oapi-codegen gives each operation's
// `default` response, e.g. DiscoverdefaultApplicationProblemPlusJSONResponse.
type defaultResponse interface {
	~struct {
		Body       Problem
		StatusCode int
	}
}

// problemResponse returns an operation's `default` response for code, for
// strict handlers to return:
//
//	return problemResponse[GetRundefaultApplicationProblemPlusJSONResponse](ctx, http.StatusNotFound, ErrorCodeNotFound, ""), nil
//
// Use it instead of a struct literal: it keeps StatusCode and Body.Status
// equal, and a StatusCode left at zero makes the generated code call
// WriteHeader(0), which panics.
func problemResponse[T defaultResponse](ctx context.Context, status int, code ErrorCode, detail string) T {
	return T{Body: buildProblem(ctx, status, code, detail), StatusCode: status}
}

// writeProblem writes p as application/problem+json with p.Status.
func writeProblem(w http.ResponseWriter, p Problem) {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(p); err != nil {
		// Problem holds only strings and numbers; this cannot fail.
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_, _ = buf.WriteTo(w)
}

// notFound answers paths and methods under BasePath that the spec does not
// define.
func (h *handler) notFound(w http.ResponseWriter, r *http.Request) {
	writeProblem(w, newProblem(r, http.StatusNotFound, ErrorCodeNotFound,
		fmt.Sprintf("没有与 %s %s 对应的接口", r.Method, r.URL.Path)))
}

// parameterError handles path, query, header and cookie parameters that the
// generated wrapper could not bind.
func (h *handler) parameterError(w http.ResponseWriter, r *http.Request, err error) {
	p := newProblem(r, http.StatusBadRequest, ErrorCodeValidationFailed, "请求参数无效")
	p.Errors = &[]FieldError{parameterFieldError(err)}
	writeProblem(w, p)
}

// requestError handles request bodies the strict server could not decode.
func (h *handler) requestError(w http.ResponseWriter, r *http.Request, err error) {
	p := newProblem(r, http.StatusBadRequest, ErrorCodeValidationFailed, "请求体无法解析")
	p.Errors = &[]FieldError{{Detail: err.Error(), Pointer: new("#")}}
	writeProblem(w, p)
}

// responseError handles errors returned by handlers and failures to encode
// their responses. Details go to the log, never to the client.
func (h *handler) responseError(w http.ResponseWriter, r *http.Request, err error) {
	ctx := r.Context()
	level, msg := slog.LevelError, "api handler failed"
	if logging.ClientGone(ctx, err) {
		// The client closed the connection; nobody reads the response.
		level, msg = slog.LevelDebug, "api request abandoned by the client"
	}
	h.log.Log(ctx, level, msg,
		"method", r.Method, "path", r.URL.Path,
		"request_id", RequestIDFromContext(ctx), "error", err)
	writeProblem(w, newProblem(r, http.StatusInternalServerError, ErrorCodeInternalError, ""))
}

// parameterFieldError maps the generated binding errors to a field error
// that names the offending parameter or header.
func parameterFieldError(err error) FieldError {
	var (
		header   *RequiredHeaderError
		required *RequiredParamError
		format   *InvalidParamFormatError
		many     *TooManyValuesForParamError
		unmarsh  *UnmarshalingParamError
		cookie   *UnescapedCookieParamError
	)
	switch {
	case errors.As(err, &header):
		return FieldError{Detail: "缺少必填的请求头", Header: new(header.ParamName)}
	case errors.As(err, &required):
		return FieldError{Detail: "缺少必填的参数", Parameter: new(required.ParamName)}
	case errors.As(err, &format):
		return FieldError{Detail: format.Err.Error(), Parameter: new(format.ParamName)}
	case errors.As(err, &many):
		return FieldError{Detail: "参数只能出现一次", Parameter: new(many.ParamName)}
	case errors.As(err, &unmarsh):
		return FieldError{Detail: unmarsh.Err.Error(), Parameter: new(unmarsh.ParamName)}
	case errors.As(err, &cookie):
		return FieldError{Detail: "cookie 无法解码", Parameter: new(cookie.ParamName)}
	default:
		// A binding error type added by a newer generator: keep the message,
		// there is no location to report.
		return FieldError{Detail: err.Error()}
	}
}
