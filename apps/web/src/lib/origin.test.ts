import { describe, expect, it } from "vitest";

import { deploymentOriginMismatch } from "./origin";

describe("deploymentOriginMismatch", () => {
  it.each([
    [
      "same origin",
      "http://mini.local:33333/",
      "http://mini.local:33333",
      null,
    ],
    [
      "path and case do not matter",
      "http://Mini.local:33333/",
      "http://mini.local:33333",
      null,
    ],
    [
      "IP instead of the name",
      "http://mini.local:33333/",
      "http://192.168.50.50:33333",
      "http://mini.local:33333",
    ],
    [
      "another port",
      "https://lerryn.example.com/",
      "http://lerryn.example.com:33333",
      "https://lerryn.example.com",
    ],
    ["no console link yet", undefined, "http://mini.local:33333", null],
    ["unparsable link", "not a url", "http://mini.local:33333", null],
  ])("%s", (_name, consoleUrl, current, want) => {
    expect(deploymentOriginMismatch(consoleUrl, current)).toBe(want);
  });
});
