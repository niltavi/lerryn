import type { Schemas } from "@lerryn/api";
import { describe, expect, it } from "vitest";

import { loginLayout, type LoginLayout } from "./login";

type Methods = Schemas["LoginMethod"][];

describe("loginLayout", () => {
  const cases: [string, { login_methods: Methods; admin_password_login?: boolean } | undefined, LoginLayout][] = [
    ["password only", { login_methods: ["password"] }, { sso: false, password: "primary" }],
    ["password and SSO", { login_methods: ["password", "oidc"] }, { sso: true, password: "primary" }],
    ["SSO with the admin entry", { login_methods: ["oidc"], admin_password_login: true }, { sso: true, password: "admin" }],
    ["SSO only", { login_methods: ["oidc"], admin_password_login: false }, { sso: true, password: "none" }],
    ["SSO, server without the capability", { login_methods: ["oidc"] }, { sso: true, password: "none" }],
    ["admin entry without SSO", { login_methods: [], admin_password_login: true }, { sso: false, password: "admin" }],
    ["discover not read yet", undefined, { sso: false, password: "primary" }],
  ];
  it.each(cases)("%s", (_name, caps, want) => {
    expect(loginLayout(caps)).toEqual(want);
  });
});
