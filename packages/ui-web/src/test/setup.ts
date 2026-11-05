import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";

// jsdom has no layout. The virtualizer and React Aria look for these.
class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
globalThis.ResizeObserver ??= ResizeObserverStub as unknown as typeof ResizeObserver;
window.matchMedia ??= ((query: string) => ({
  matches: false,
  media: query,
  onchange: null,
  addEventListener: () => {},
  removeEventListener: () => {},
  addListener: () => {},
  removeListener: () => {},
  dispatchEvent: () => false,
})) as unknown as typeof window.matchMedia;
Element.prototype.scrollTo ??= function scrollTo() {};
// jsdom defines window.scrollTo but only to log "not implemented"; the router calls it.
window.scrollTo = () => {};
// Give elements a size so the log viewer's virtualizer renders rows.
// The scroll viewport is 560 px tall and each row 20 px.
const heightOf = (el: Element) => (el.classList.contains("log-body") ? 560 : 20);
Element.prototype.getBoundingClientRect = function getBoundingClientRect() {
  const height = heightOf(this);
  return { x: 0, y: 0, top: 0, left: 0, right: 800, bottom: height, width: 800, height, toJSON() {} } as DOMRect;
};
Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
  configurable: true,
  get(this: HTMLElement) {
    return heightOf(this);
  },
});
Object.defineProperty(HTMLElement.prototype, "offsetWidth", { configurable: true, get: () => 800 });
Object.defineProperty(Element.prototype, "clientHeight", {
  configurable: true,
  get(this: Element) {
    return heightOf(this);
  },
});
Object.defineProperty(Element.prototype, "clientWidth", { configurable: true, get: () => 800 });

afterEach(() => cleanup());
