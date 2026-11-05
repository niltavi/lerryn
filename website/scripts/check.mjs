import assert from 'node:assert/strict';
import { readFile, stat } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { dirname, resolve, sep } from 'node:path';
import { spawnSync } from 'node:child_process';
import { languages, origin } from '../content.mjs';

const dist = resolve(fileURLToPath(new URL('../dist/', import.meta.url)));
for (const c of Object.values(languages)) {
  const file = resolve(dist, c.path, 'index.html');
  const html = await readFile(file, 'utf8');
  assert(html.startsWith('<!doctype html>'), `${c.lang}: missing doctype`);
  assert(html.includes(`<html lang="${c.lang}">`), `${c.lang}: wrong language`);
  assert.equal([...html.matchAll(/<h1\b/g)].length, 1, `${c.lang}: expected one h1`);
  assert(html.includes(`<link rel="canonical" href="${origin}/${c.path}">`), `${c.lang}: wrong canonical`);
  const base = c.path ? '../' : './';
  for (const link of [
    `<link rel="icon" href="${base}assets/lerryn-icon-circle-32.png" type="image/png" sizes="32x32">`,
    `<link rel="icon" href="${base}assets/lerryn-icon-circle.svg" type="image/svg+xml" sizes="any">`,
    `<link rel="apple-touch-icon" href="${base}assets/lerryn-icon-circle-180.png" sizes="180x180">`,
  ]) assert(html.includes(link), `${c.lang}: missing circular site icon: ${link}`);
  for (const language of ['en', 'zh-CN', 'x-default']) {
    assert(html.includes(`hreflang="${language}"`), `${c.lang}: missing alternate ${language}`);
  }
  const ids = [...html.matchAll(/\bid="([^"]+)"/g)].map((m) => m[1]);
  assert.equal(new Set(ids).size, ids.length, `${c.lang}: duplicate element IDs`);
  for (const reference of html.matchAll(/\b(?:aria-controls|aria-labelledby)="([^"]+)"/g)) {
    for (const id of reference[1].split(' ')) assert(ids.includes(id), `${c.lang}: broken ARIA reference ${id}`);
  }
  for (const image of html.matchAll(/<img\b[^>]*>/g)) {
    assert(/\balt="[^"]*"/.test(image[0]), `${c.lang}: missing image alt`);
    assert(/\bwidth="\d+"/.test(image[0]) && /\bheight="\d+"/.test(image[0]), `${c.lang}: missing image dimensions`);
  }
  for (const button of html.matchAll(/<button\b[^>]*>/g)) {
    assert(button[0].includes('type="button"'), `${c.lang}: implicit button type`);
  }
  for (const match of html.matchAll(/\b(?:href|src)="([^"]+)"/g)) {
    const href = match[1];
    if (/^https:/.test(href)) continue;
    assert(!/^[a-z]+:/i.test(href), `${c.lang}: unexpected URL protocol`);
    const [path, hash] = href.split('#');
    let target = path ? resolve(dirname(file), path) : file;
    assert(target === dist || target.startsWith(dist + sep), `${c.lang}: asset escapes output directory: ${href}`);
    if ((await stat(target)).isDirectory()) target = resolve(target, 'index.html');
    await stat(target);
    if (hash) {
      const content = target === file ? html : await readFile(target, 'utf8');
      assert(content.includes(`id="${hash}"`), `${c.lang}: broken anchor ${href}`);
    }
  }
  const text = html.replace(/<[^>]*>/g, ' ');
  assert(!/\b(?:TODO|placeholder|coming soon|lorem ipsum|mock data)\b|占位|尚未实现/i.test(text), `${c.lang}: unfinished visible content`);
  assert(!html.includes('github.com/yarin-zhang/'), `${c.lang}: old repository owner`);
  console.log(`${c.lang}: metadata, local links, images, landmarks, and ARIA references passed`);
}
const css = await readFile(resolve(dist, 'assets/site.css'), 'utf8');
for (const asset of css.matchAll(/url\(['"]?(\.\/[^)'"\s]+)['"]?\)/g)) {
  const path = resolve(dist, 'assets', asset[1]);
  assert(path.startsWith(dist + (dist.endsWith(sep) ? '' : sep)));
  await stat(path);
}
const luminance = (hex) => {
  const full = hex.length === 4 ? [...hex.slice(1)].map((c) => c + c).join('') : hex.slice(1);
  const channels = full.match(/../g).map((s) => parseInt(s, 16) / 255).map((v) => v <= .04045 ? v / 12.92 : ((v + .055) / 1.055) ** 2.4);
  return channels[0] * .2126 + channels[1] * .7152 + channels[2] * .0722;
};
const paletteBlocks = [
  css.match(/:root\s*\{([^}]+)\}/)[1],
  css.match(/:root\[data-theme=['"]dark['"]\]\s*\{([^}]+)\}/)[1],
];
for (const block of paletteBlocks) {
  const palette = Object.fromEntries([...block.matchAll(/--([\w-]+):\s*(#[\da-f]{6}|#[\da-f]{3})(?![\da-f])/gi)].map((m) => [m[1], m[2]]));
  const contrast = (x, y) => { const a = luminance(palette[x]), b = luminance(palette[y]); return (Math.max(a, b) + .05) / (Math.min(a, b) + .05); };
  for (const background of ['paper', 'surface']) {
    for (const color of ['ink', 'muted', 'link']) assert(contrast(color, background) >= 4.5, `Text contrast failed: ${color} on ${background}`);
    // Ochre only colors large serif heading words, which need 3:1.
    assert(contrast('ochre', background) >= 3, `Heading accent contrast failed on ${background}`);
  }
  for (const color of ['ink', 'link']) assert(contrast(color, 'surface-2') >= 4.5, `Text contrast failed: ${color} on surface-2`);
  assert(contrast('ink', 'surface-3') >= 4.5, 'Code contrast failed');
  assert(contrast('accent', 'on-accent') >= 4.5, 'Button contrast failed');
}
assert((await readFile(resolve(dist, 'robots.txt'), 'utf8')).includes(`${origin}/sitemap.xml`));
console.log('Local font assets, sitemap discovery, and light/dark text contrast passed');
const pagingTests = spawnSync(process.execPath, ['--test', fileURLToPath(new URL('./paging.test.mjs', import.meta.url))], { stdio: 'inherit' });
assert.equal(pagingTests.status, 0, 'Slide navigation tests failed');
