package service

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/niltavi/lerryn/server/internal/runproto"
)

// CredentialSpec is a credential a manifest references (the
// ManifestCredential schema). The reference name is all a task version
// knows; which credential it resolves to is decided by grants, so filling
// in or replacing a credential never creates a new version (tech design
// 5.3).
type CredentialSpec struct {
	Name string `json:"name"`
	// As is "env" (the default) or "file".
	As          string `json:"as,omitempty"`
	Description string `json:"description,omitempty"`
	Optional    bool   `json:"optional,omitempty"`
}

// Credential rules.
const (
	maxManifestCredentials = 20
	maxCredentialDesc      = 200
)

// credentialNamePattern matches CredentialFieldName and the reference
// names: they become environment variable and file names.
var credentialNamePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// reservedEnvNames are environment variables the platform sets in every
// task container. A credential field may not take one of these names,
// since injecting it would override what the runner set.
var reservedEnvNames = map[string]bool{
	"PATH": true, "HOME": true, "VIRTUAL_ENV": true,
	"PYTHONPATH": true, "PYTHONUNBUFFERED": true, "PYTHONDONTWRITEBYTECODE": true,
}

// reservedEnvName reports whether a credential field may not use name.
func reservedEnvName(name string) bool {
	return reservedEnvNames[name] || strings.HasPrefix(name, "LERRYN_") || strings.HasPrefix(name, "LD_")
}

func (c CredentialSpec) as() string {
	if c.As == "" {
		return runproto.InjectEnv
	}
	return c.As
}

func validateCredentialSpecs(v *validation, specs []CredentialSpec) {
	const at = "#/manifest/credentials"
	if len(specs) > maxManifestCredentials {
		v.content(at, fmt.Sprintf("最多引用 %d 个凭证", maxManifestCredentials))
		return
	}
	seen := map[string]bool{}
	for i, c := range specs {
		where := fmt.Sprintf("%s/%d", at, i)
		switch {
		case !credentialNamePattern.MatchString(c.Name):
			v.content(where+"/name", "引用名为 1 到 64 个字符，只能包含大写字母、数字和下划线，以大写字母开头")
		case seen[c.Name]:
			v.content(where+"/name", fmt.Sprintf("引用名 %s 重复", c.Name))
		}
		seen[c.Name] = true
		switch c.As {
		case "", runproto.InjectEnv, runproto.InjectFile:
		default:
			v.content(where+"/as", "注入方式只能是 env 或 file")
		}
		if utf8.RuneCountInString(c.Description) > maxCredentialDesc {
			v.content(where+"/description", fmt.Sprintf("用途说明最多 %d 个字符", maxCredentialDesc))
		}
	}
}

func credentialRefs(specs []CredentialSpec) []runproto.CredentialRef {
	if len(specs) == 0 {
		return nil
	}
	refs := make([]runproto.CredentialRef, len(specs))
	for i, c := range specs {
		refs[i] = runproto.CredentialRef{Name: c.Name, As: c.as(), Optional: c.Optional}
	}
	return refs
}
