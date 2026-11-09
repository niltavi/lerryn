package httpapi

import (
	"context"
	"slices"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/service"
)

// Discover reports the build, API version, capabilities, runtime catalogue,
// matching Skill version and the caller's identity.
func (h *handler) Discover(ctx context.Context, _ DiscoverRequestObject) (DiscoverResponseObject, error) {
	caps := h.cloneCapabilities()
	if h.accounts != nil {
		// Open registration is an admin setting; first_account lets the
		// console tell the first registrant they become the admin.
		open, first, err := h.accounts.SignupState(ctx)
		if err != nil {
			return nil, err
		}
		caps.RegistrationOpen, caps.FirstAccount = open || first, first
		// With local password login off, the password form is for admins
		// only: clients lead with single sign-on and keep a way in for
		// admins.
		forAll, err := h.accounts.PasswordLoginForAll(ctx)
		if err != nil {
			return nil, err
		}
		if !forAll && slices.Contains(caps.LoginMethods, LoginMethodPassword) {
			caps.LoginMethods = slices.DeleteFunc(caps.LoginMethods, func(m LoginMethod) bool { return m == LoginMethodPassword })
			caps.AdminPasswordLogin = new(true)
		}
	}
	if caps.AdminPasswordLogin == nil {
		caps.AdminPasswordLogin = new(false)
	}
	items, err := skillSummaries()
	if err != nil {
		return nil, err
	}
	return Discover200JSONResponse{
		Server:       h.server,
		API:          APIInfo{Version: APIVersion, SpecVersion: SpecVersion},
		Capabilities: caps,
		// The same catalogue the task service validates manifests against.
		Runtimes: runtimes(),
		// Skills ship with the server, so their version is the server's.
		Skills: SkillCatalog{Version: h.server.Version, Items: items},
		// Null for anonymous calls; with a token, agents use it to confirm
		// the token works and to see its scopes.
		Principal: toPrincipal(auth.PrincipalFrom(ctx)),
		// The address the console was opened at, when it is one of the
		// deployment's, so the console only warns about a foreign one.
		Links: Links{Console: h.consoleBase(ctx) + "/"},
	}, nil
}

func (h *handler) GetEnvironment(_ context.Context, _ GetEnvironmentRequestObject) (GetEnvironmentResponseObject, error) {
	environment := h.environment
	if environment == "" {
		environment = EnvironmentInfoEnvironmentUnspecified
	}
	return GetEnvironment200JSONResponse{Environment: environment}, nil
}

func runtimes() []RuntimeInfo {
	catalogue := service.Runtimes()
	out := make([]RuntimeInfo, len(catalogue))
	for i, r := range catalogue {
		out[i] = RuntimeInfo{Language: r.Language, Version: r.Version, Lockfile: optional(r.Lockfile)}
	}
	return out
}

// cloneCapabilities copies the slices so a response never aliases the
// handler's configuration, and nil slices encode as [] rather than null.
func (h *handler) cloneCapabilities() Capabilities {
	c := h.capabilities
	c.LoginMethods = append([]LoginMethod{}, c.LoginMethods...)
	c.NetworkPolicies = append([]NetworkPolicy{}, c.NetworkPolicies...)
	return c
}
