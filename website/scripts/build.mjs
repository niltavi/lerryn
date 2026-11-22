import { cp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { resolve } from 'node:path';
import { languages, origin, repository } from '../content.mjs';
import * as art from './art.mjs';

const root = fileURLToPath(new URL('../../', import.meta.url));
const dist = resolve(root, 'website/dist');
const escape = (value) => String(value).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const arrow = '<span aria-hidden="true">↗</span>';
const lines = (s) => s.split('\n').map(escape).join('<br>');
const heading = (s) => lines(s).replace(/\*([^*]+)\*/g, '<em>$1</em>');
const body = (s) => escape(s).replace(/\*([^*]+)\*/g, '<strong>$1</strong>');

await rm(dist, { recursive: true, force: true });
await mkdir(dist, { recursive: true });
await cp(resolve(root, 'website/assets'), resolve(dist, 'assets'), { recursive: true });
await cp(resolve(root, 'design/brand/logo/svg/lerryn-mark.svg'), resolve(dist, 'assets/lerryn-mark.svg'));
await cp(resolve(root, 'design/brand/logo/svg/lerryn-icon-circle.svg'), resolve(dist, 'assets/lerryn-icon-circle.svg'));
for (const size of [32, 180]) {
  await cp(resolve(root, `design/brand/logo/png/lerryn-icon-circle-${size}.png`), resolve(dist, `assets/lerryn-icon-circle-${size}.png`));
}
await cp(resolve(root, 'design/brand/logo/png/lerryn-logo-square.png'), resolve(dist, 'assets/lerryn-logo-square.png'));

// Use the same checked-in Niltavi UI palette as the console build.
const palette = await readFile(resolve(root, 'packages/ui-web/styles/theme/palette.css'), 'utf8');
const canvasTokens = ['paper', 'throat-975'].map((name) => {
  const declaration = palette.match(new RegExp('^\\s*--color-' + name + ': [^;]+;', 'm'))?.[0].trim();
  if (!declaration) throw new Error('Missing Niltavi UI palette token: ' + name);
  return declaration;
});
await writeFile(resolve(dist, 'assets/canvas.css'), ':root { ' + canvasTokens.join(' ') + ' }\n');

// The published Gravity UI glyphs used by Niltavi UI's header.
const headerIcon = async (name, className = '') =>
  (await readFile(resolve(root, 'website/art/controls', name + '.svg'), 'utf8'))
    .trim()
    .replace('<svg ', '<svg aria-hidden="true" focusable="false" ' + (className ? 'class="' + className + '" ' : ''));
const [moon, sun, globe, github] = await Promise.all([
  headerIcon('moon', 'moon'), headerIcon('sun', 'sun-icon'), headerIcon('globe'), headerIcon('logo-github'),
]);

const mark = (await readFile(resolve(root, 'design/brand/logo/svg/lerryn-mark.svg'), 'utf8'))
  .replace(/<title>.*?<\/title>/, '')
  .replace(/ width="\d+" height="\d+"/, ' class="mark" aria-hidden="true" focusable="false"')
  // Inline paths take theme colors from CSS, so the mark needs no light plate on dark pages.
  .replace('fill="#245761"', 'class="mark-side"')
  .replace('fill="#428c9a"', 'class="mark-top"');

// Vendored niltavi wordmark; its three colors map to theme tokens.
const wordmarkFill = { '#1B2235': 'nv-ink', '#3A5CF4': 'nv-accent', '#DD6300': 'nv-dot' };
const niltavi = (await readFile(resolve(root, 'website/art/niltavi-wordmark.svg'), 'utf8'))
  .replace(/<title[^>]*>.*?<\/title>\s*<desc>.*?<\/desc>/s, '')
  .replace(/ role="img" aria-labelledby="[^"]*"/, ' class="niltavi-wordmark" aria-hidden="true" focusable="false"')
  .replace(/fill="(#[0-9A-F]{6})"/g, (_, hex) => `class="${wordmarkFill[hex]}"`);

for (const c of Object.values(languages)) {
  const base = c.path ? '../' : './';
  // Each page links the documentation in its own language.
  const docs = `${repository}/blob/main/docs/${c.docs}`;
  const url = `${origin}/${c.path}`;
  const page = `<!doctype html>
<html lang="${c.lang}">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="color-scheme" content="light dark">
  <title>${escape(c.title)}</title>
  <meta name="description" content="${escape(c.description)}">
  <link rel="canonical" href="${url}">
  <link rel="alternate" hreflang="en" href="${origin}/">
  <link rel="alternate" hreflang="zh-CN" href="${origin}/zh/">
  <link rel="alternate" hreflang="x-default" href="${origin}/">
  <meta property="og:type" content="website">
  <meta property="og:site_name" content="Lerryn">
  <meta property="og:title" content="${escape(c.title)}">
  <meta property="og:description" content="${escape(c.description)}">
  <meta property="og:url" content="${url}">
  <meta property="og:locale" content="${c.lang === 'en' ? 'en_US' : 'zh_CN'}">
  <meta property="og:image" content="${origin}/assets/lerryn-logo-square.png">
  <meta property="og:image:width" content="1024">
  <meta property="og:image:height" content="1024">
  <meta property="og:image:alt" content="Lerryn">
  <meta name="twitter:card" content="summary">
  <link rel="alternate" type="text/plain" href="${base}llms.txt" title="Installation guide for agents">
  <link rel="icon" href="${base}assets/lerryn-icon-circle-32.png" type="image/png" sizes="32x32">
  <link rel="icon" href="${base}assets/lerryn-icon-circle.svg" type="image/svg+xml" sizes="any">
  <link rel="apple-touch-icon" href="${base}assets/lerryn-icon-circle-180.png" sizes="180x180">
  <link rel="preload" href="${base}assets/fonts/cormorant-garamond-latin-400-normal.woff2" as="font" type="font/woff2" crossorigin>
  <script src="${base}assets/theme.js"></script>
  <link rel="stylesheet" href="${base}assets/canvas.css">
  <link rel="stylesheet" href="${base}assets/site.css">
  <script src="${base}assets/site.js" defer></script>
  <script src="${base}assets/paging.js" defer></script>
</head>
<body data-theme-dark="${escape(c.themeDark)}" data-theme-light="${escape(c.themeLight)}">
  <a class="skip" href="#main">${escape(c.skip)}</a>
  <div class="shell">
    <header class="masthead">
      <a class="brand" href="${base}${c.path}" aria-label="Lerryn"><span class="brand-mark">${mark}</span><span class="wordmark">Lerryn</span></a>
      <div class="tools">
        <button class="icon-button theme-switch" type="button" aria-label="${escape(c.themeDark)}" title="${escape(c.themeDark)}" hidden>
          ${moon}${sun}
        </button>
        <details class="language-menu">
          <summary class="icon-button" aria-label="${escape(c.language)}" title="${escape(c.language)}">
            ${globe}
          </summary>
          <ul class="language-menu__list">
            ${Object.values(languages).map((locale) => `<li><a href="${base}${locale.path}" lang="${locale.lang}" hreflang="${locale.lang}"${locale.lang === c.lang ? ' aria-current="page"' : ''}>${locale.lang === 'en' ? 'English' : '简体中文'}</a></li>`).join('')}
          </ul>
        </details>
        <a class="icon-button" href="${repository}" aria-label="${escape(c.github)}" title="${escape(c.github)}">
          ${github}
        </a>
      </div>
    </header>
    <main id="main" tabindex="-1">
      <section class="slide cover" id="cover" aria-labelledby="hero-title">
        <div class="slide-copy">
          <h1 id="hero-title">${heading(c.cover.heading)}</h1>
          <p>${body(c.cover.text)}</p>
          <div class="actions"><a class="button primary" href="#connect">${escape(c.primary)}</a><a class="button soft" href="${repository}">${escape(c.secondary)} ${arrow}</a></div>
        </div>
        <div class="slide-art">${art.cover(escape(c.cover.art))}</div>
      </section>
      ${c.slides.map((s) => `<section class="slide" id="${s.id}" aria-labelledby="${s.id}-title">
        <div class="slide-copy"><h2 id="${s.id}-title">${heading(s.heading)}</h2><p>${body(s.text)}</p></div>
        <div class="slide-art">${art[s.id](escape(s.art))}</div>
      </section>`).join('\n      ')}
      <section class="slide connect" id="connect" aria-labelledby="connect-title">
        <div class="slide-copy"><h2 id="connect-title">${heading(c.connect.heading)}</h2><p>${body(c.connect.text)}</p><a class="text-link" href="${docs}getting-started.md">${escape(c.connect.guide)} ${arrow}</a></div>
        <figure class="slide-art prompt" data-copied="${escape(c.copied)}" data-copy-error="${escape(c.copyError)}">
          <figcaption class="prompt-label">${escape(c.connect.label)}</figcaption>
          <blockquote class="prompt-text">${escape(c.connect.prompt)}</blockquote>
          <div class="prompt-actions"><span class="copy-message" role="status" aria-live="polite"></span><button class="button primary copy" type="button" hidden>${escape(c.copy)}</button></div>
        </figure>
      </section>
      <section class="slide end" id="end" aria-labelledby="end-title">
        <div class="slide-copy"><span class="dash" aria-hidden="true"></span><h2 id="end-title">${heading(c.end.heading)}</h2><p>${body(c.end.text)}</p>
          <div class="actions"><a class="button primary" href="#connect">${escape(c.primary)}</a><a class="button soft" href="${docs}deploy.md">${escape(c.deploy)} ${arrow}</a></div>
        </div>
      </section>
    </main>
    <footer><a class="niltavi" href="https://niltavi.com/" aria-label="Niltavi">${niltavi}</a></footer>
  </div>
</body>
</html>
`;
  const target = resolve(dist, c.path);
  await mkdir(target, { recursive: true });
  await writeFile(resolve(target, 'index.html'), page);
}
await cp(resolve(root, 'website/llms.txt'), resolve(dist, 'llms.txt'));
await writeFile(resolve(dist, 'robots.txt'), `User-agent: *\nAllow: /\n\nSitemap: ${origin}/sitemap.xml\n`);
await writeFile(resolve(dist, 'sitemap.xml'), `<?xml version="1.0" encoding="UTF-8"?>\n<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">${Object.values(languages).map((c) => `<url><loc>${origin}/${c.path}</loc></url>`).join('')}</urlset>\n`);
await writeFile(resolve(dist, '_headers'), `/*\n  X-Content-Type-Options: nosniff\n  Referrer-Policy: strict-origin-when-cross-origin\n  X-Frame-Options: DENY\n  Content-Security-Policy: default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; font-src 'self'; connect-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'\n`);
console.log('Built English and Chinese pages in website/dist for ' + origin);
