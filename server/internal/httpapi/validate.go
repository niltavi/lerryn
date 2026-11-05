package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"

	"github.com/niltavi/lerryn/api"
	"github.com/niltavi/lerryn/server/internal/auth"
)

const (
	// maxRequestBytes caps request bodies. The largest is applyTask, whose
	// files may total 1 MiB before JSON escaping.
	maxRequestBytes = 4 << 20
	// maxFieldErrors caps the field errors in one validation_failed answer.
	maxFieldErrors = 20
)

// contractRouter finds the operation of a request in the embedded contract.
// The bundled spec is covered by tests, so failing to read it is a build
// defect.
var contractRouter = func() routers.Router {
	doc, err := openapi3.NewLoader().LoadFromData(api.Spec)
	if err != nil {
		panic(fmt.Sprintf("httpapi: load the embedded spec: %v", err))
	}
	// Match on paths only; the servers list names an example host.
	doc.Servers = nil
	router, err := legacy.NewRouter(doc)
	if err != nil {
		panic(fmt.Sprintf("httpapi: route the embedded spec: %v", err))
	}
	return router
}()

// schemaPrinter formats schema violations. Field details stay in English,
// like the other messages from parsers and validators.
var schemaPrinter = message.NewPrinter(language.English)

// validate checks parameters and request bodies against the embedded
// contract before the generated code decodes them (tech design 9.3). The
// strict server only binds parameters and decodes JSON: without this,
// missing required fields, unknown properties and out-of-range values
// would reach the handlers. MCP tool calls pass through here as well.
//
// Requests the caller may not make are passed on unchecked, so that the
// access check answers them and a caller without access learns nothing
// from validation errors. Requests that match no operation are passed on
// for the router's not_found.
func (h *handler) validate(next http.Handler) http.Handler {
	opts := &openapi3filter.Options{
		MultiError: true,
		// Handlers apply defaults themselves; the request stays as sent.
		SkipSettingDefaults: true,
		// Access is checked by authenticate and requireAccess. The filter
		// still checks the security requirements, against the principal
		// those set, so that a request can never pass validation through a
		// requirement nobody checked.
		AuthenticationFunc: checkScheme,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
		}
		route, params, err := contractRouter.FindRoute(r)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		rules, known := operations[operationKey(route.Operation.OperationID)]
		if !known {
			next.ServeHTTP(w, r)
			return
		}
		if status, _ := denial(auth.PrincipalFrom(r.Context()), rules); status != 0 {
			next.ServeHTTP(w, r)
			return
		}
		err = openapi3filter.ValidateRequest(r.Context(), &openapi3filter.RequestValidationInput{
			Request:    r,
			PathParams: params,
			Route:      route,
			Options:    opts,
		})
		if err != nil {
			writeProblem(w, h.validationProblem(r, err))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// checkScheme accepts a security scheme when the request's principal was
// authenticated by it.
func checkScheme(ctx context.Context, in *openapi3filter.AuthenticationInput) error {
	p := auth.PrincipalFrom(ctx)
	switch {
	case p == nil:
	case in.SecuritySchemeName == "accessToken" && p.Method == auth.MethodAccessToken,
		in.SecuritySchemeName == "sessionCookie" && p.Method == auth.MethodSession:
		return nil
	}
	return in.NewError(nil)
}

// validationProblem maps what openapi3filter reports to a problem with
// one field error per violation.
func (h *handler) validationProblem(r *http.Request, err error) Problem {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return newProblem(r, http.StatusRequestEntityTooLarge, ErrorCodeValidationFailed,
			fmt.Sprintf("请求体超过 %d MiB", maxRequestBytes>>20))
	}
	var security *openapi3filter.SecurityRequirementsError
	if errors.As(err, &security) {
		// Unreachable while the access check runs first; answer as it would.
		return newProblem(r, http.StatusUnauthorized, ErrorCodeUnauthenticated, "需要访问令牌或登录会话")
	}

	var fields []FieldError
	collectFieldErrors(err, "", &fields)
	if len(fields) > maxFieldErrors {
		fields = fields[:maxFieldErrors]
	}
	p := newProblem(r, http.StatusBadRequest, ErrorCodeValidationFailed, "请求不符合接口契约")
	p.Errors = &fields
	return p
}

// collectFieldErrors walks what openapi3filter reports and appends one field
// error per violation, located by the parameter name or by a JSON pointer
// into the body. Details come from the reasons only: the full messages of
// schema errors quote the offending value, which may be a password.
func collectFieldErrors(err error, param string, out *[]FieldError) {
	add := func(pointer, detail string) {
		f := FieldError{Detail: detail}
		if param != "" {
			f.Parameter = new(param)
		} else {
			f.Pointer = new("#" + pointer)
		}
		*out = append(*out, f)
	}
	// A type switch, not errors.As: the walk visits each level of the tree,
	// and errors.As would jump past a MultiError to its first element.
	switch e := err.(type) { //nolint:errorlint // see above
	case openapi3.MultiError:
		for _, x := range e {
			collectFieldErrors(x, param, out)
		}
	case *openapi3filter.RequestError:
		if e.Parameter != nil {
			param = e.Parameter.Name
		}
		var schemaErr *openapi3.SchemaError
		var parseErr *openapi3filter.ParseError
		switch {
		case errors.As(e.Err, &schemaErr):
			collectFieldErrors(e.Err, param, out)
		case errors.As(e.Err, &parseErr):
			add("", "无法解析："+parseErr.Reason)
		case e.Reason != "":
			add("", e.Reason)
		case e.Err != nil:
			add("", e.Err.Error())
		}
	case *openapi3.SchemaError:
		// Errors converted from the JSON Schema 2020-12 validator group
		// the violations below them in Origin.
		var sub openapi3.MultiError
		if errors.As(e.Origin, &sub) {
			collectFieldErrors(sub, param, out)
			return
		}
		var verr *jsonschema.ValidationError
		if errors.As(e, &verr) && len(verr.Causes) == 0 {
			add(jsonPointer(e.JSONPointer()), verr.ErrorKind.LocalizedString(schemaPrinter))
			return
		}
		add(jsonPointer(e.JSONPointer()), e.Reason)
	default:
		if err != nil {
			add("", "请求无效")
		}
	}
}

// jsonPointer encodes unescaped reference tokens as an RFC 6901 pointer.
func jsonPointer(tokens []string) string {
	var b strings.Builder
	for _, t := range tokens {
		b.WriteByte('/')
		b.WriteString(strings.NewReplacer("~", "~0", "/", "~1").Replace(t))
	}
	return b.String()
}
