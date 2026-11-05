import { describe, expect, it } from "vitest";

import { safeRedirect } from "./redirect";

describe("safeRedirect", () => {
  it.each([
    ["/runs/0192f3a1", "/runs/0192f3a1"],
    ["/settings/tokens?new=true#x", "/settings/tokens?new=true#x"],
    ["/", "/"],
  ])("keeps the site path %s", (input, want) => {
    expect(safeRedirect(input)).toBe(want);
  });

  it.each([
    "https://evil.example/",
    "//evil.example/path",
    "/\\evil.example",
    "javascript:alert(1)",
    "runs/abc",
    "/login",
    "/register?redirect=/x",
    "/\u0000x",
    123,
    undefined,
  ])("rejects %s", (input) => {
    expect(safeRedirect(input)).toBeUndefined();
  });
});
