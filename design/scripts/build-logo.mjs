#!/usr/bin/env node
// 由 Logo 矢量母版 design/brand/logo/svg/lerryn-mark.svg 生成：
//   design/brand/logo/svg/   横向、正方形、圆形与网站图标
//   design/brand/logo/png/   对应的 PNG（用本机的 Google Chrome 渲染；找不到 Chrome 时跳过并提示）
//   design/ui-native/src/components/logoMark.ts、packages/ui-web/src/brand/logoMark.ts   RN、Web Logo 组件用的路径
// 只改母版，然后在仓库根目录运行 `pnpm design:logo`。
// Chrome 不在默认位置时，用 CHROME_PATH 环境变量指定。

import { execFileSync } from 'node:child_process';
import { existsSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..'); // design/
const svgDir = join(root, 'brand/logo/svg');
const pngDir = join(root, 'brand/logo/png');

const master = readFileSync(join(svgDir, 'lerryn-mark.svg'), 'utf8');
const viewBox = /viewBox="0 0 ([\d.]+) ([\d.]+)"/.exec(master);
const paths = [...master.matchAll(/<path fill="(#[0-9a-fA-F]{6})" d="([^"]+)"\s*\/>/g)].map(([, fill, d]) => ({ fill, d }));
if (!viewBox || paths.length !== 2) {
  throw new Error('lerryn-mark.svg 应当是 viewBox="0 0 W H"，并且正好两条 path：先深青侧面，后溪水蓝绿顶面');
}
const W = Number(viewBox[1]);
const H = Number(viewBox[2]);
const [bark, kraft] = paths;
const PLATE = '#fef8ef';
const PAD = 48;
// 正方形、圆形版本里图形占的宽度（画布 1024）
const BADGE = { square: 800, circle: 736 };
// Match niltavi.com's paper circle and fine rim without changing the logo paths.
const ICON = { paper: '#f5f1e6', border: '#dfd8c7', radius: 492, stroke: 12 };

const mark = paths.map((p) => `<path fill="${p.fill}" d="${p.d}"/>`).join('');
const svg = (w, h, body) =>
  `<svg xmlns="http://www.w3.org/2000/svg" width="${w}" height="${h}" viewBox="0 0 ${w} ${h}"><title>Lerryn</title>${body}</svg>\n`;
const placed = (width) => {
  const s = width / W;
  return `<g transform="translate(${(512 - width / 2).toFixed(2)} ${(512 - (H * s) / 2).toFixed(2)}) scale(${s.toFixed(5)})">${mark}</g>`;
};

const variants = {
  'lerryn-logo-horizontal': { w: W + PAD * 2, h: H + PAD * 2, body: `<g transform="translate(${PAD} ${PAD})">${mark}</g>`, png: 2 },
  'lerryn-logo-square': { w: 1024, h: 1024, body: `<rect width="1024" height="1024" fill="${PLATE}"/>${placed(BADGE.square)}`, png: 1 },
  'lerryn-logo-circle': { w: 1024, h: 1024, body: `<circle cx="512" cy="512" r="512" fill="${PLATE}"/>${placed(BADGE.circle)}`, png: 1 },
  'lerryn-icon-circle': { w: 1024, h: 1024, body: `<circle cx="512" cy="512" r="${ICON.radius}" fill="${ICON.paper}" stroke="${ICON.border}" stroke-width="${ICON.stroke}"/>${placed(BADGE.circle)}`, png: 1 },
};

for (const [name, v] of Object.entries(variants)) {
  writeFileSync(join(svgDir, `${name}.svg`), svg(v.w, v.h, v.body));
}

const logoMarkTs = `// 由 design/scripts/build-logo.mjs 根据 design/brand/logo/svg/lerryn-mark.svg 生成，不要手改。
// 颜色固定为 logo 原色，不随主题变化。

export const LOGO_MARK = {
  width: ${W},
  height: ${H},
  kraft: { fill: '${kraft.fill}', d: '${kraft.d}' },
  bark: { fill: '${bark.fill}', d: '${bark.d}' },
} as const;

export const LOGO_PLATE = '${PLATE}';

/** 正方形、圆形版本里图形占的宽度（以 1024 画布计） */
export const LOGO_BADGE = { square: ${BADGE.square}, circle: ${BADGE.circle} } as const;
`;
// RN 和 Web 两个组件库各用一份，内容相同。
writeFileSync(join(root, 'ui-native/src/components/logoMark.ts'), logoMarkTs);
writeFileSync(join(root, '../packages/ui-web/src/brand/logoMark.ts'), logoMarkTs);

const chrome = process.env.CHROME_PATH ?? '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
if (!existsSync(chrome)) {
  console.log('svg 与 logoMark.ts 已更新。没找到 Chrome，跳过 PNG；设置 CHROME_PATH 后重跑即可导出。');
  process.exit(0);
}

const work = mkdtempSync(join(tmpdir(), 'lerryn-logo-'));
const exports = Object.entries(variants).map(([name, v]) => ({ name, source: name, w: v.w * v.png, h: v.h * v.png }));
for (const size of [32, 180]) {
  exports.push({ name: `lerryn-icon-circle-${size}`, source: 'lerryn-icon-circle', w: size, h: size });
}
for (const { name, source, w, h } of exports) {
  const page = join(work, `${name}.html`);
  writeFileSync(
    page,
    `<!doctype html><html><head><meta charset="utf-8"><style>html,body{margin:0;background:transparent}img{display:block;width:${w}px;height:${h}px}</style></head><body><img src="file://${join(svgDir, `${source}.svg`)}"></body></html>`,
  );
  execFileSync(
    chrome,
    [
      '--headless=new',
      '--disable-gpu',
      '--hide-scrollbars',
      '--allow-file-access-from-files',
      '--default-background-color=00000000',
      '--force-device-scale-factor=1',
      `--user-data-dir=${join(work, `profile-${name}`)}`,
      `--window-size=${w},${h}`,
      `--screenshot=${join(pngDir, `${name}.png`)}`,
      `file://${page}`,
    ],
    { stdio: 'ignore' },
  );
}
console.log('svg、png 与 logoMark.ts 已更新。');
