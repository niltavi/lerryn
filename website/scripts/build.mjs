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

await rm(dist, { recursive: true, force: true });
await mkdir(dist, { recursive: true });
await cp(resolve(root, 'website/assets'), resolve(dist, 'assets'), { recursive: true });
await cp(resolve(root, 'design/brand/logo/svg/lerryn-mark.svg'), resolve(dist, 'assets/lerryn-mark.svg'));
await cp(resolve(root, 'design/brand/logo/png/lerryn-logo-square.png'), resolve(dist, 'assets/lerryn-logo-square.png'));

const mark = (await readFile(resolve(root, 'design/brand/logo/svg/lerryn-mark.svg'), 'utf8'))
  .replace(/<title>.*?<\/title>/, '')
  .replace(/ width="\d+" height="\d+"/, ' class="mark" aria-hidden="true" focusable="false"');

for (const c of Object.values(languages)) {
  const base = c.path ? '../' : './';
  const docs = `${repository}/blob/main/docs/`;
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
  <link rel="icon" href="${base}assets/lerryn-mark.svg" type="image/svg+xml">
  <link rel="preload" href="${base}assets/fonts/cormorant-garamond-latin-400-normal.woff2" as="font" type="font/woff2" crossorigin>
  <link rel="stylesheet" href="${base}assets/site.css">
  <script src="${base}assets/site.js" defer></script>
</head>
<body data-theme-dark="${escape(c.themeDark)}" data-theme-light="${escape(c.themeLight)}">
  <a class="skip" href="#main">${escape(c.skip)}</a>
  <div class="shell">
    <header class="masthead">
      <a class="brand" href="${base}index.html" aria-label="Lerryn"><span class="mark-plate">${mark}</span><span class="wordmark">Lerryn</span><span class="family-tag">${escape(c.family)}</span></a>
      <div class="tools">
        <button class="icon-button theme-switch" type="button" aria-label="${escape(c.themeDark)}" title="${escape(c.themeDark)}" hidden>
          <svg class="moon" viewBox="0 0 24 24" aria-hidden="true"><path d="M20 15.3A8.7 8.7 0 0 1 8.7 4 8.8 8.8 0 1 0 20 15.3Z"/></svg>
          <svg class="sun-icon" viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="4"/><path d="M12 2v2m0 16v2M2 12h2m16 0h2M5 5l1.5 1.5m11 11L19 19M5 19l1.5-1.5m11-11L19 5"/></svg>
        </button>
        <a class="icon-button" href="${base}${c.alternate}index.html" lang="${c.lang === 'en' ? 'zh-CN' : 'en'}" hreflang="${c.lang === 'en' ? 'zh-CN' : 'en'}" aria-label="${c.alternateLabel}" title="${c.alternateLabel}">
          <svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="9.5"/><path d="M12 2.5c-5 5.2-5 13.8 0 19M12 2.5c5 5.2 5 13.8 0 19M2.5 12h19"/></svg>
        </a>
        <a class="icon-button" href="${repository}" aria-label="${escape(c.github)}" title="${escape(c.github)}">
          <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M15 22v-4a4.8 4.8 0 0 0-1-3.5c3 0 6-2 6-5.5.08-1.25-.27-2.48-1-3.5.28-1.15.28-2.35 0-3.5 0 0-1 0-3 1.5-2.64-.5-5.36-.5-8 0C6 2 5 2 5 2c-.3 1.15-.3 2.35 0 3.5A5.4 5.4 0 0 0 4 9c0 3.5 3 5.5 6 5.5-.39.49-.68 1.05-.85 1.65S8.93 17.38 9 18v4"/><path d="M9 18c-4.51 2-5-2-7-2"/></svg>
        </a>
      </div>
    </header>
    <main id="main" tabindex="-1">
      <section class="slide cover" aria-labelledby="hero-title">
        <div class="slide-copy">
          <h1 id="hero-title">${heading(c.cover.heading)}</h1>
          <p>${escape(c.cover.text)}</p>
          <div class="actions"><a class="button primary" href="${docs}deploy.md">${escape(c.primary)} ${arrow}</a><a class="button soft" href="${repository}">${escape(c.secondary)} ${arrow}</a></div>
        </div>
        <div class="slide-art">${art.cover(escape(c.cover.art))}</div>
      </section>
      ${c.slides.map((s) => `<section class="slide" id="${s.id}" aria-labelledby="${s.id}-title">
        <div class="slide-copy"><h2 id="${s.id}-title">${heading(s.heading)}</h2><p>${escape(s.text)}</p></div>
        <div class="slide-art">${art[s.id](escape(s.art))}</div>
      </section>`).join('\n      ')}
      <section class="slide connect" id="connect" aria-labelledby="connect-title">
        <div class="slide-copy"><h2 id="connect-title">${heading(c.connect.heading)}</h2><p>${escape(c.connect.text)}</p><a class="text-link" href="${docs}cli.md">${c.lang === 'en' ? 'CLI &amp; agent guide' : 'CLI 与 Agent 接入文档'} ${arrow}</a></div>
        <div class="slide-art code-example" data-copy="${escape(c.copy)}" data-copied="${escape(c.copied)}" data-copy-error="${escape(c.copyError)}">
          <div class="tabs" role="tablist" aria-label="${escape(c.tabsLabel)}" hidden>${c.snippets.map((s, i) => `<button type="button" role="tab" id="tab-${s.id}" aria-controls="panel-${s.id}" aria-selected="${i === 0}" tabindex="${i === 0 ? '0' : '-1'}">${s.label}</button>`).join('')}</div>
          ${c.snippets.map((s) => `<section class="code-panel" id="panel-${s.id}" aria-labelledby="tab-${s.id}" tabindex="0"><pre><code>${escape(s.code)}</code></pre><button class="copy" type="button" hidden>${escape(c.copy)}</button></section>`).join('')}
          <span class="copy-message" role="status" aria-live="polite"></span>
        </div>
      </section>
      <section class="slide end" aria-labelledby="end-title">
        <div class="slide-copy"><span class="dash" aria-hidden="true"></span><h2 id="end-title">${heading(c.end.heading)}</h2><p>${escape(c.end.text)}</p>
          <div class="actions"><a class="button primary" href="${docs}deploy.md">${escape(c.primary)} ${arrow}</a><a class="button soft" href="${repository}">${escape(c.secondary)} ${arrow}</a></div>
        </div>
      </section>
    </main>
    <footer><span>© Niltavi</span></footer>
  </div>
</body>
</html>
`;
  const target = resolve(dist, c.path);
  await mkdir(target, { recursive: true });
  await writeFile(resolve(target, 'index.html'), page);
}
await writeFile(resolve(dist, 'robots.txt'), `User-agent: *\nAllow: /\n\nSitemap: ${origin}/sitemap.xml\n`);
await writeFile(resolve(dist, 'sitemap.xml'), `<?xml version="1.0" encoding="UTF-8"?>\n<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">${Object.values(languages).map((c) => `<url><loc>${origin}/${c.path}</loc></url>`).join('')}</urlset>\n`);
await writeFile(resolve(dist, '_headers'), `/*\n  X-Content-Type-Options: nosniff\n  Referrer-Policy: strict-origin-when-cross-origin\n  X-Frame-Options: DENY\n  Content-Security-Policy: default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; font-src 'self'; connect-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'\n`);
console.log('Built English and Chinese pages in website/dist for ' + origin);
