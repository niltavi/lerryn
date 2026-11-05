#!/usr/bin/env node
// Generates Web, Native, prototype and credential-page themes from the pinned
// Niltavi snapshot and product token mappings.
//   design/ui-native/styles/tokens.css   Uniwind 主题变量（Lerryn token + HeroUI Native 变量映射）
//   design/ui-native/src/theme/tokens.ts  JS 侧的同一份数值（SVG、动画等拿不到 className 的地方用）
//   packages/ui-web/styles/tokens.css     Web Tailwind and HeroUI semantics
//   design/prototype/console.css          marked plain CSS token block
//   server/internal/web/theme.css         standalone credential page tokens
// 改设计 token 只改 tokens.json，然后在仓库根目录运行 `pnpm design:tokens`。

import { readFileSync, writeFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..'); // design/
const tokens = JSON.parse(readFileSync(resolve(root, 'tokens.json'), 'utf8'));
const niltavi = JSON.parse(readFileSync(resolve(root, 'niltavi-ui/tokens.json'), 'utf8'));

const themes = tokens.color.themes.map((t) => t.id);
const first = themes[0];

const valueFor = (token, theme) => {
  if (token.source) {
    const value = niltavi.resolved[theme]?.[token.source];
    if (!value) throw new Error(`Unknown Niltavi token: ${theme}.${token.source}`);
    return value;
  }
  return typeof token.value === 'string' ? token.value : token.value[theme] ?? token.value[first];
};

const colors = Object.fromEntries(
  themes.map((theme) => [
    theme,
    Object.fromEntries(tokens.color.tokens.map((t) => [t.name, valueFor(t, theme)])),
  ]),
);
const shadows = Object.fromEntries(
  themes.map((theme) => [
    theme,
    Object.fromEntries(tokens.shadow.tokens.map((t) => [t.name, valueFor(t, theme)])),
  ]),
);

// HeroUI Native 的语义变量 → Lerryn token。左边是 HeroUI 读的名字，右边是 tokens.json 里的名字。
const HEROUI_COLORS = {
  background: 'paper',
  foreground: 'ink',
  surface: 'liner',
  'surface-foreground': 'ink',
  'surface-secondary': 'liner-2',
  'surface-secondary-foreground': 'ink',
  'surface-tertiary': 'liner-3',
  'surface-tertiary-foreground': 'ink',
  overlay: 'liner',
  'overlay-foreground': 'ink',
  backdrop: 'backdrop',
  muted: 'ink-muted',
  default: 'liner-2',
  'default-foreground': 'ink',
  accent: 'kraft-strong',
  'accent-foreground': 'on-kraft',
  'field-background': 'liner',
  'field-foreground': 'ink',
  'field-placeholder': 'ink-muted',
  'field-border': 'line-strong',
  success: 'success',
  'success-foreground': 'on-kraft',
  warning: 'warning',
  'warning-foreground': 'on-warning',
  danger: 'danger',
  'danger-foreground': 'on-danger',
  segment: 'liner',
  'segment-foreground': 'ink',
  border: 'line',
  separator: 'line',
  focus: 'focus',
  link: 'kraft-strong',
};
// Preserve upstream semantics, strengthening values used as small domain labels.
const HEROUI_OVERRIDES = Object.fromEntries(themes.map((theme) => [theme, {
  ...niltavi.resolved[theme],
  // Stronger borders and placeholder text meet the product's contrast requirements.
  'field-border': colors[theme]['line-strong'],
  focus: colors[theme].focus,
  success: colors[theme].success,
  danger: colors[theme].danger,
  'field-placeholder': colors[theme]['ink-muted'],
  'surface-foreground': colors[theme].ink,
  'surface-secondary-foreground': colors[theme].ink,
  'surface-tertiary-foreground': colors[theme].ink,
  'overlay-foreground': colors[theme].ink,
}]));

const HEROUI_SHADOWS = {
  'surface-shadow': 'flute-b',
  'overlay-shadow': 'flute-a',
  'field-shadow': 'flute-e',
};

const HEROUI_EXTRA = {
  'accent-soft': 'kraft-soft',
  'accent-soft-foreground': 'kraft-strong',
  'accent-soft-hover': 'liner-3',
  'default-soft': 'liner-2',
  'default-soft-foreground': 'ink',
  'default-soft-hover': 'liner-3',
  'success-soft-foreground': 'success',
  'warning-soft-foreground': 'warning-ink',
  'danger-soft-foreground': 'danger',
  'surface-hover': 'liner-2',
  'default-hover': 'liner-3',
};

const px = (v) => v;
const pad = '      ';

const themeBlock = (theme) => {
  const lines = [];
  lines.push(`${pad}/* Lerryn token */`);
  for (const [name, value] of Object.entries(colors[theme])) lines.push(`${pad}--${name}: ${value};`);
  for (const [name, value] of Object.entries(shadows[theme])) lines.push(`${pad}--${name}: ${value};`);
  lines.push('', `${pad}/* HeroUI Native 语义变量 */`);
  for (const [hero, value] of Object.entries(heroColors(theme))) {
    lines.push(`${pad}--${hero}: ${value};`);
  }
  for (const [hero, ours] of Object.entries(HEROUI_SHADOWS)) lines.push(`${pad}--${hero}: ${shadows[theme][ours]};`);
  return lines.join('\n');
};

const fontFamilies = {
  display: 'DMSans_700Bold',
  'display-semibold': 'DMSans_600SemiBold',
  'display-heavy': 'DMSans_800ExtraBold',
  mono: 'IBMPlexMono_400Regular',
  'mono-medium': 'IBMPlexMono_500Medium',
};

const css = `/* 由 design/scripts/sync-tokens.mjs 根据 design/tokens.json 生成，不要手改。 */

@theme {
  --radius: 0.5rem;
  --field-radius: 0.75rem;
  --field-border-width: 1px;
  --border-width: 1px;
  --opacity-disabled: 0.45;

${Object.entries(fontFamilies)
  .map(([k, v]) => `  --font-${k}: '${v}';`)
  .join('\n')}

${tokens.radius.tokens.map((t) => `  --${t.name}: ${t.value};`).join('\n')}
}

@layer theme {
  :root {
${themes
  .map((theme) => `    @variant ${theme} {\n${themeBlock(theme)}\n    }`)
  .join('\n\n')}
  }
}

/* 让 Lerryn token 也能直接写成 className：bg-kraft、text-ink-muted、border-line-strong… */
@theme inline static {
${tokens.color.tokens.map((t) => `  --color-${t.name}: var(--${t.name});`).join('\n')}

${tokens.shadow.tokens.map((t) => `  --shadow-${t.name}: var(--${t.name});`).join('\n')}
}
`;

writeFileSync(resolve(root, 'ui-native/styles/tokens.css'), css);

const typeStyles = Object.fromEntries(
  tokens.type.groups.flatMap((g) =>
    g.styles.map((s) => [
      s.name,
      {
        family: s.family ?? g.family,
        fontSize: parseFloat(s.fontSize),
        lineHeight: parseFloat(s.lineHeight),
        fontWeight: String(s.fontWeight),
      },
    ]),
  ),
);

const list = (family) => Object.fromEntries(tokens[family].tokens.map((t) => [t.name, parseFloat(px(t.value))]));

const ts = `// 由 design/scripts/sync-tokens.mjs 根据 design/tokens.json 生成，不要手改。

export const themes = ${JSON.stringify(themes)} as const;
export type LerrynTheme = (typeof themes)[number];

export const colors = ${JSON.stringify(colors, null, 2)} as const;
export type LerrynColor = keyof (typeof colors)['light'];

// Legacy depth names remain available for existing component callers.
export const flute = { E: 0, B: 0, C: 0, A: 0 } as const;
export type Flute = keyof typeof flute;

export const spacing = ${JSON.stringify(list('spacing'), null, 2)} as const;

export const radius = ${JSON.stringify(list('radius'), null, 2)} as const;

export const fontFamily = ${JSON.stringify(fontFamilies, null, 2)} as const;

export const typeStyles = ${JSON.stringify(typeStyles, null, 2)} as const;
export type LerrynTypeStyle = keyof typeof typeStyles;
`;

writeFileSync(resolve(root, 'ui-native/src/theme/tokens.ts'), ts);

// ── Web：packages/ui-web/styles/tokens.css ──
// HeroUI v3（Web）读的是同名的 CSS 变量，主题由 <html> 上的 data-theme 或 .dark 类切换。
// 输出不放进 @layer，这样能盖过 HeroUI 写在 theme 层里的默认值。
// HeroUI Web 直接读这些柔和色变量，不经过 --color-*，所以和 Native 不同，要在主题块里给定值。

function heroColors(theme) {
  const mapped = Object.fromEntries(Object.entries({ ...HEROUI_COLORS, ...HEROUI_EXTRA })
    .map(([hero, ours]) => [hero, colors[theme][ours]]));
  return { ...mapped, ...HEROUI_OVERRIDES[theme] };
}

const webThemeBlock = (theme) => {
  const pad2 = '  ';
  const lines = [`${pad2}color-scheme: ${theme};`, '', `${pad2}/* Lerryn token */`];
  for (const [name, value] of Object.entries(colors[theme])) lines.push(`${pad2}--${name}: ${value};`);
  for (const [name, value] of Object.entries(shadows[theme])) lines.push(`${pad2}--${name}: ${value};`);
  lines.push('', `${pad2}/* HeroUI v3 语义变量 */`);
  for (const [hero, value] of Object.entries(heroColors(theme))) {
    lines.push(`${pad2}--${hero}: ${value};`);
  }
  for (const [hero, ours] of Object.entries(HEROUI_SHADOWS)) lines.push(`${pad2}--${hero}: ${shadows[theme][ours]};`);
  return lines.join('\n');
};
const indent = (text) =>
  text
    .split('\n')
    .map((l) => (l ? `  ${l}` : l))
    .join('\n');

const webType = tokens.type.groups.flatMap((g) =>
  g.styles.map((s) => {
    const lines = [`  --text-${s.name}: ${s.fontSize};`, `  --text-${s.name}--line-height: ${s.lineHeight};`];
    lines.push(`  --text-${s.name}--font-weight: ${s.fontWeight};`);
    if (s.letterSpacing) lines.push(`  --text-${s.name}--letter-spacing: ${s.letterSpacing};`);
    return lines.join('\n');
  }),
);

const [lightTheme, darkTheme] = themes;
const webCss = `/* 由 design/scripts/sync-tokens.mjs 根据 design/tokens.json 生成，不要手改。 */

/* 让 Lerryn token 直接写成 Tailwind 类：bg-paper、text-ink-muted、shadow-flute-b、text-title-2、font-mono… */
@theme inline {
${tokens.color.tokens.map((t) => `  --color-${t.name}: var(--${t.name});`).join('\n')}

${tokens.shadow.tokens.map((t) => `  --shadow-${t.name}: var(--${t.name});`).join('\n')}

${Object.entries(tokens.type.families)
  .map(([k, v]) => `  --font-${k}: ${v};`)
  .join('\n')}

${tokens.radius.tokens.map((t) => `  --${t.name}: ${t.value};`).join('\n')}

${webType.join('\n')}
}

:root {
${tokens.spacing.tokens.map((t) => `  --${t.name}: ${t.value};`).join('\n')}
}

:root,
.${lightTheme},
[data-theme="${lightTheme}"] {
${webThemeBlock(lightTheme)}
}

.${darkTheme},
[data-theme="${darkTheme}"] {
${webThemeBlock(darkTheme)}
}

/* 脚本设置 data-theme 之前，先按系统偏好显示，避免首屏闪白 */
@media (prefers-color-scheme: dark) {
  :root:not([data-theme]):not(.${lightTheme}) {
${indent(indent(webThemeBlock(darkTheme)))}
  }
}
`;

writeFileSync(resolve(root, '../packages/ui-web/styles/tokens.css'), webCss);

// The static prototype consumes plain CSS rather than Tailwind directives.
const prototypePath = resolve(root, 'prototype/console.css');
const prototype = readFileSync(prototypePath, 'utf8');
const declarations = (theme) => [
  ...Object.entries(colors[theme]),
  ...Object.entries(shadows[theme]),
  ...Object.entries(tokens.type.families).map(([name, value]) => [`font-${name}`, value]),
  ...tokens.spacing.tokens.map((t) => [t.name, t.value]),
  ...tokens.radius.tokens.map((t) => [t.name, t.value]),
].map(([name, value]) => `  --${name}: ${value};`).join('\n');
if (!prototype.includes('/* BEGIN GENERATED TOKENS */')) throw new Error('Prototype token markers are missing');
const prototypeTokens = `/* BEGIN GENERATED TOKENS */\n:root, .wl-app {\n${declarations(lightTheme)}\n}\n.wl-app.theme-dark {\n${declarations(darkTheme)}\n}\n/* END GENERATED TOKENS */`;
writeFileSync(prototypePath, prototype.replace(/\/\* BEGIN GENERATED TOKENS \*\/[\s\S]*?\/\* END GENERATED TOKENS \*\//, prototypeTokens));

const fillCss = `/* Code generated by design/scripts/sync-tokens.mjs. DO NOT EDIT. */
:root {
  color-scheme: light;
${declarations(lightTheme)}
}
@media (prefers-color-scheme: dark) {
  :root {
    color-scheme: dark;
${indent(indent(declarations(darkTheme)))}
  }
}
`;
writeFileSync(resolve(root, '../server/internal/web/theme.css'), fillCss);

console.log(
  `tokens: ${tokens.color.tokens.length} colors × ${themes.length} themes, ${tokens.shadow.tokens.length} flat aliases → web, native, prototype, credential page`,
);
