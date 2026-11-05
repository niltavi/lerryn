import { readFileSync } from "node:fs";

import AxeBuilder from "@axe-core/playwright";
import { test as base, expect, type Locator, type Page } from "@playwright/test";

import { Api, login } from "./support/api.ts";
import type { E2EState } from "./support/seed.ts";

function readState(): E2EState {
  const file = process.env.LERRYN_E2E_STATE;
  if (!file) throw new Error("LERRYN_E2E_STATE is not set; run the suite through playwright.config.ts");
  return JSON.parse(readFileSync(file, "utf8")) as E2EState;
}

export interface Fixtures {
  state: E2EState;
  /** Alice's agent: the access token the seed used. */
  agent: Api;
  /** Who the page is signed in as; null for a signed-out page. */
  signedInAs: "alice" | "admin" | null;
}

export const test = base.extend<Fixtures>({
  // eslint-disable-next-line no-empty-pattern
  state: async ({}, use) => use(readState()),
  agent: async ({ state }, use) => use(new Api(state.base).withToken(state.alice.token)),
  signedInAs: ["alice", { option: true }],
  baseURL: async ({ state }, use) => use(state.base),
  // Each test signs in afresh, so a test that signs out leaves the others alone.
  context: async ({ context, state, signedInAs }, use) => {
    if (signedInAs) {
      const who = state[signedInAs];
      const cookie = await login(state.base, who.username, who.password);
      const [name, value] = cookie.split("=", 2) as [string, string];
      await context.addCookies([{ name, value, url: state.base, httpOnly: true, sameSite: "Lax" }]);
    }
    await use(context);
  },
});

export { expect };

/** Fails on serious or critical axe findings, listing them. */
export async function expectAccessible(page: Page, exclude: string[] = []) {
  let builder = new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]);
  for (const sel of exclude) builder = builder.exclude(sel);
  const { violations } = await builder.analyze();
  const serious = violations.filter((v) => v.impact === "serious" || v.impact === "critical");
  expect(
    serious.map((v) => `${v.id} (${v.impact}): ${v.help}\n  ${v.nodes.map((n) => n.target.join(" ")).slice(0, 5).join("\n  ")}`),
  ).toEqual([]);
}

/**
 * Whether a row is inside the visible part of its scrolling log. The log
 * scrolls on its own, so the page viewport (toBeInViewport) does not tell.
 */
export async function inLogView(row: Locator): Promise<boolean> {
  if ((await row.count()) === 0) return false;
  return row.first().evaluate((el) => {
    const box = el.closest(".log-body")!.getBoundingClientRect();
    const r = el.getBoundingClientRect();
    return r.bottom > box.top + 1 && r.top < box.bottom - 1;
  });
}

export async function expectInLogView(row: Locator, visible = true) {
  await expect.poll(() => inLogView(row), { message: `${row} ${visible ? "in" : "out of"} the log view` }).toBe(visible);
}

/** The page has no horizontal scroll. */
export async function expectNoHorizontalScroll(page: Page) {
  const overflow = await page.evaluate(() => {
    const el = document.scrollingElement ?? document.documentElement;
    const wide = [...document.querySelectorAll<HTMLElement>("body *")]
      .filter((n) => n.getBoundingClientRect().right > window.innerWidth + 1 && getComputedStyle(n).position !== "fixed")
      .slice(0, 5)
      .map((n) => `${n.tagName.toLowerCase()}.${[...n.classList].join(".")}`);
    return { scroll: el.scrollWidth - el.clientWidth, wide };
  });
  expect(overflow.scroll, `wider than the viewport: ${overflow.wide.join(", ")}`).toBeLessThanOrEqual(0);
}
