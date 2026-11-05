import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const root = fileURLToPath(new URL('../../', import.meta.url));
const read = (path) => readFileSync(new URL(`../../${path}`, import.meta.url), 'utf8');
const definition = JSON.parse(read('design/tokens.json'));
const upstream = JSON.parse(read('design/niltavi-ui/tokens.json'));
const colors = (theme) => Object.fromEntries(definition.color.tokens.map((token) => [
  token.name, token.source ? upstream.resolved[theme][token.source] : token.value[theme],
]));

function luminance(hex) {
  assert.match(hex, /^#[\da-f]{6}$/i);
  const rgb = hex.slice(1).match(/../g).map((c) => parseInt(c, 16) / 255)
    .map((c) => c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4);
  return rgb[0] * 0.2126 + rgb[1] * 0.7152 + rgb[2] * 0.0722;
}
function contrast(fg, bg) {
  const a = luminance(fg), b = luminance(bg);
  return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
}

for (const theme of ['light', 'dark']) {
  test(`${theme}: labels, status text and control boundaries remain distinguishable`, () => {
    const c = colors(theme);
    const pairs = [
      ['ink', 'paper'], ['ink', 'liner'], ['ink', 'liner-2'], ['ink', 'liner-3'],
      ['ink-muted', 'paper'], ['ink-muted', 'liner'], ['ink-muted', 'liner-2'],
      ['kraft-strong', 'paper'], ['kraft-strong', 'kraft-soft'],
      ['on-kraft', 'kraft'], ['on-kraft', 'kraft-strong'],
      ['success', 'success-soft'], ['danger', 'danger-soft'],
      ['run', 'run-soft'], ['warning-ink', 'warning-soft'],
      ['on-warning', 'warning'], ['on-danger', 'danger'],
    ];
    for (const [fg, bg] of pairs) {
      const ratio = contrast(c[fg], c[bg]);
      assert.ok(ratio >= 4.5, `${theme}: ${fg} on ${bg}: ${ratio.toFixed(2)} < 4.5`);
    }
    for (const bg of ['paper', 'liner', 'liner-2', 'liner-3']) {
      for (const fg of ['line-strong', 'focus']) {
        const ratio = contrast(c[fg], c[bg]);
        assert.ok(ratio >= 3, `${theme}: ${fg} on ${bg}: ${ratio.toFixed(2)} < 3`);
      }
    }
  });
}

test('regeneration preserves checked-in web, native, prototype and credential page themes', () => {
  const files = [
    'packages/ui-web/styles/tokens.css', 'design/ui-native/styles/tokens.css',
    'design/ui-native/src/theme/tokens.ts', 'design/prototype/console.css',
    'server/internal/web/theme.css',
  ];
  const before = files.map(read);
  execFileSync(process.execPath, ['design/scripts/sync-tokens.mjs'], { cwd: root });
  for (const [i, path] of files.entries()) assert.equal(read(path), before[i], `${path} is stale`);
});
