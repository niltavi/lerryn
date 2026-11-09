import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { runInNewContext } from 'node:vm';
import test from 'node:test';

const source = await readFile(new URL('../assets/paging.js', import.meta.url), 'utf8');
const setup = ({ width = 1280, heights = [800, 800, 800], reduced = false } = {}) => {
  const listeners = {};
  const classes = new Set();
  const calls = [];
  let now = 0;
  let resize;
  const window = {
    innerWidth: width, innerHeight: 800, scrollY: 0,
    matchMedia: () => ({ matches: reduced }),
    addEventListener: (name, callback) => { listeners[name] = callback; },
    scrollTo: (options) => { calls.push(options); },
  };
  let top = 0;
  const slides = heights.map((height) => {
    const offset = top;
    top += height;
    return { getBoundingClientRect: () => ({ top: offset - window.scrollY, height }) };
  });
  const footer = { getBoundingClientRect: () => ({ top: top - window.scrollY, height: 80 }) };
  const root = {
    scrollHeight: top + 80,
    classList: { toggle: (name, on) => on ? classes.add(name) : classes.delete(name) },
  };
  const document = {
    documentElement: root,
    querySelectorAll: () => slides,
    querySelector: () => footer,
    addEventListener: (name, callback) => { listeners[name] = callback; },
  };
  window.ResizeObserver = class { constructor(callback) { resize = callback; } observe() {} };
  runInNewContext(source, {
    window, document, performance: { now: () => now },
    ResizeObserver: window.ResizeObserver,
  });
  const dispatch = (name, properties) => {
    const event = { deltaX: 0, deltaY: 0, deltaMode: 0, target: { closest: () => false }, ...properties };
    event.preventDefault = () => { event.defaultPrevented = true; };
    listeners[name](event);
    return event.defaultPrevented === true;
  };
  return {
    window, classes, calls, slides,
    wheel: (properties) => dispatch('wheel', properties),
    key: (properties) => dispatch('keydown', properties),
    advance: (ms) => { now += ms; },
    land: () => { window.scrollY = calls.at(-1).top; listeners.scrollend(); },
    resize: () => resize(),
  };
};

test('one trackpad gesture lands on one slide, including a long momentum tail', () => {
  const page = setup();
  assert(page.classes.has('deck'));
  for (const deltaY of [4, 8, 12]) page.wheel({ deltaY });
  assert.equal(page.calls.length, 1);
  assert.equal(page.calls[0].top, 800);
  page.land();
  for (let i = 0; i < 20; i++) {
    page.advance(80);
    page.wheel({ deltaY: 4 });
  }
  assert.equal(page.calls.length, 1);
  page.advance(200);
  page.wheel({ deltaY: 30 });
  assert.equal(page.calls.at(-1).top, 1600);
  page.land();
  page.advance(200);
  page.wheel({ deltaY: -30 });
  assert.equal(page.calls.at(-1).top, 800);
});

test('normalizes mouse line/page deltas and ignores small or reversed noise', () => {
  for (const deltaMode of [1, 2]) {
    const page = setup();
    page.wheel({ deltaY: 3, deltaMode });
    assert.equal(page.calls[0].top, 800);
  }
  const page = setup();
  page.wheel({ deltaY: 20 });
  page.wheel({ deltaY: -4 });
  page.wheel({ deltaY: 8 });
  assert.equal(page.calls.length, 0);
  page.advance(200);
  page.wheel({ deltaY: 20 });
  assert.equal(page.calls.length, 0);
});

test('does not interrupt zoom, horizontal gestures, or previously handled events', () => {
  const page = setup();
  for (const properties of [{ ctrlKey: true }, { metaKey: true }, { deltaX: 100 }, { defaultPrevented: true }]) {
    const prevented = page.wheel({ deltaY: 40, ...properties });
    assert.equal(prevented, properties.defaultPrevented === true);
  }
  assert.equal(page.calls.length, 0);
});

test('narrow screens and overflowing slides keep native scrolling', () => {
  for (const options of [{ width: 390 }, { heights: [800, 1000, 800] }]) {
    const page = setup(options);
    assert(!page.classes.has('deck'));
    assert(!page.wheel({ deltaY: 80 }));
    assert(!page.key({ key: 'PageDown' }));
    assert.equal(page.calls.length, 0);
  }
  const page = setup();
  page.slides[1].getBoundingClientRect = () => ({ top: 800, height: 1000 });
  page.resize();
  assert(!page.classes.has('deck'));
});

test('keyboard navigation reaches slides and footer, including from between slides', () => {
  for (const [key, shiftKey, start, expected] of [
    ['ArrowDown', false, 0, 800], ['PageDown', false, 300, 800],
    [' ', false, 800, 1600], [' ', true, 1600, 800],
    ['ArrowUp', false, 1600, 800], ['PageUp', false, 1100, 800],
    ['End', false, 0, 1680], ['Home', false, 1600, 0],
  ]) {
    const page = setup();
    page.window.scrollY = start;
    assert(page.key({ key, shiftKey }));
    assert.equal(page.calls.at(-1).top, expected);
  }
  const page = setup();
  page.window.scrollY = 1600;
  page.wheel({ deltaY: 40 });
  assert.equal(page.calls.at(-1).top, 1680);
  page.land();
  page.advance(200);
  page.wheel({ deltaY: -40 });
  assert.equal(page.calls.at(-1).top, 1600);
});

test('focused controls retain their keyboard actions', () => {
  const page = setup();
  assert(!page.key({ key: ' ', target: { closest: () => true } }));
  assert(!page.key({ key: 'Home', ctrlKey: true }));
  assert(!page.key({ key: 'Tab' }));
  assert.equal(page.calls.length, 0);
});

test('an explicit key can reverse an unfinished transition', () => {
  const page = setup();
  page.wheel({ deltaY: 40 });
  page.window.scrollY = 400;
  page.key({ key: 'ArrowUp', repeat: true });
  assert.equal(page.calls.length, 1);
  page.key({ key: 'ArrowUp' });
  assert.equal(page.calls.at(-1).top, 0);
});

test('animation guard expires without scrollend and reduced motion is instant', () => {
  const page = setup();
  page.wheel({ deltaY: 40 });
  page.advance(250);
  page.wheel({ deltaY: 40 });
  assert.equal(page.calls.length, 1);
  page.advance(500);
  page.window.scrollY = 800;
  page.wheel({ deltaY: 40 });
  assert.equal(page.calls.at(-1).top, 1600);
  const reduced = setup({ reduced: true });
  reduced.wheel({ deltaY: 40 });
  assert.equal(reduced.calls[0].behavior, 'instant');
});
