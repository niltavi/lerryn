package httpapi

import (
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/niltavi/lerryn/api"
	"github.com/niltavi/lerryn/server/internal/auth"
)

// operation holds what the access checks need to know about an operation.
// It is read from the contract, so the spec stays the single place where
// an operation's kind and security are declared.
type operation struct {
	// kind is the x-operation-kind: read, mutate or execute.
	kind string
	// scope is the token scope the operation needs, from x-operation-kind.
	scope auth.Scope
	// anonymous is true when the operation's security allows an empty
	// requirement, so it can be called without credentials.
	anonymous bool
	// sessionOnly is true when the operation accepts only the session
	// cookie, such as creating an access token.
	sessionOnly bool
	// hook is true when the operation accepts only a trigger token. Such
	// operations are served by NewHooks, and the trigger service checks
	// the token.
	hook bool
}

// operations maps operationKey(operationId) to its access rules. The
// bundled spec is embedded and covered by tests, so failing to read it is a
// build defect.
var operations = func() map[string]operation {
	ops, err := loadOperations(api.Spec)
	if err != nil {
		panic(fmt.Sprintf("httpapi: read operations from the embedded spec: %v", err))
	}
	return ops
}()

var kindScopes = map[string]auth.Scope{
	"read":    auth.ScopeRead,
	"mutate":  auth.ScopeWrite,
	"execute": auth.ScopeExecute,
}

var httpMethods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

func loadOperations(spec []byte) (map[string]operation, error) {
	type requirement = map[string][]string
	type op struct {
		OperationID string         `yaml:"operationId"`
		Kind        string         `yaml:"x-operation-kind"`
		Security    *[]requirement `yaml:"security"`
	}
	var doc struct {
		Security []requirement                   `yaml:"security"`
		Paths    map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(spec, &doc); err != nil {
		return nil, err
	}
	ops := map[string]operation{}
	for path, item := range doc.Paths {
		for method, node := range item {
			if !slices.Contains(httpMethods, method) {
				continue // parameters, summary and other path item fields
			}
			var o op
			if err := node.Decode(&o); err != nil {
				return nil, fmt.Errorf("%s %s: %w", method, path, err)
			}
			scope, ok := kindScopes[o.Kind]
			if !ok {
				return nil, fmt.Errorf("%s: x-operation-kind %q", o.OperationID, o.Kind)
			}
			security := doc.Security
			if o.Security != nil {
				security = *o.Security
			}
			rules := operation{kind: o.Kind, scope: scope, sessionOnly: len(security) > 0, hook: len(security) > 0}
			for _, req := range security {
				if len(req) == 0 {
					rules.anonymous = true
				}
				if _, ok := req["sessionCookie"]; !ok || len(req) != 1 {
					rules.sessionOnly = false
				}
				if _, ok := req["triggerToken"]; !ok || len(req) != 1 {
					rules.hook = false
				}
			}
			key := operationKey(o.OperationID)
			if _, dup := ops[key]; dup {
				return nil, fmt.Errorf("operationId %s collides with another after normalisation", o.OperationID)
			}
			ops[key] = rules
		}
	}
	return ops, nil
}

// operationKey normalises an operation name. The generated strict server
// passes middleware the Go method name (getMe becomes GetMe, listApiKeys
// becomes ListAPIKeys), which differs from the operationId only in case and
// separators.
func operationKey(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return -1
		}
	}, name)
}
