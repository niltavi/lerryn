import { describe, expect, it } from "vitest";

import { en } from "./en";
import { matchLocale, translate } from "./i18n";
import { zhCN } from "./zh-CN";

function leaves(node: unknown, prefix = ""): string[] {
  if (typeof node === "string") return [prefix];
  return Object.entries(node as Record<string, unknown>).flatMap(([k, v]) =>
    leaves(v, prefix ? `${prefix}.${k}` : k),
  );
}

describe("catalogs", () => {
  it("have the same keys", () => {
    expect(leaves(en).sort()).toEqual(leaves(zhCN).sort());
  });

  it("keep the same placeholders in every message", () => {
    const placeholders = (s: string) => (s.match(/\{\w+\}/g) ?? []).sort();
    for (const key of leaves(zhCN)) {
      const get = (cat: unknown) => key.split(".").reduce<unknown>((n, k) => (n as Record<string, unknown>)[k], cat);
      expect(placeholders(get(en) as string), key).toEqual(placeholders(get(zhCN) as string));
    }
  });

  it("follow the copy rules", () => {
    for (const key of leaves(zhCN)) {
      const text = key.split(".").reduce<unknown>((n, k) => (n as Record<string, unknown>)[k], zhCN) as string;
      expect(text, key).not.toMatch(/[!！]/);
      expect(text, key).not.toContain("——");
      // A space between CJK and Latin letters or digits (placeholders count as Latin).
      expect(text, key).not.toMatch(/[一-鿿][A-Za-z0-9{]|[A-Za-z0-9}][一-鿿]/);
    }
  });
});

describe("translate", () => {
  it("fills placeholders", () => {
    expect(translate("zh-CN", "log.earlier", { count: "12,400" })).toBe("更早 12,400 行");
    expect(translate("en", "tokens.expiresIn", { count: 28 })).toBe("Expires in 28 days");
  });
  it("leaves unknown placeholders as they are", () => {
    expect(translate("zh-CN", "log.earlier")).toBe("更早 {count} 行");
  });
  it("matches browser languages", () => {
    expect(matchLocale("zh-TW")).toBe("zh-CN");
    expect(matchLocale("en-GB")).toBe("en");
    expect(matchLocale("fr")).toBe("en");
    expect(matchLocale(undefined)).toBe("zh-CN");
  });
});
