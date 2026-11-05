import { cp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { resolve } from 'node:path';
import { languages, origin, repository } from '../content.mjs';
import { plate } from './plate.mjs';

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
      <nav aria-label="${escape(c.navLabel)}">
        <a class="nav-flow" href="#how-it-works">${escape(c.nav[0])}</a>
        <a class="nav-flow" href="#connect">${escape(c.nav[1])}</a>
        <a href="${docs}deploy.md">${escape(c.nav[2])} ${arrow}</a>
      </nav>
      <div class="preferences">
        <a class="language" href="${base}${c.alternate}index.html" lang="${c.lang === 'en' ? 'zh-CN' : 'en'}" hreflang="${c.lang === 'en' ? 'zh-CN' : 'en'}">${c.alternateLabel}</a>
        <button class="theme-switch" type="button" aria-label="${escape(c.themeDark)}" hidden>
          <svg class="moon" viewBox="0 0 24 24" aria-hidden="true"><path d="M20 15.3A8.7 8.7 0 0 1 8.7 4 8.8 8.8 0 1 0 20 15.3Z"/></svg>
          <svg class="sun-icon" viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="4"/><path d="M12 2v2m0 16v2M2 12h2m16 0h2M5 5l1.5 1.5m11 11L19 19M5 19l1.5-1.5m11-11L19 5"/></svg>
        </button>
      </div>
    </header>
    <main id="main" tabindex="-1">
      <section class="hero" aria-labelledby="hero-title">
        <div class="hero-copy">
          <span class="dash" aria-hidden="true"></span>
          <h1 id="hero-title">${heading(c.heading)}</h1>
          <p class="intro">${escape(c.intro)}</p>
          <div class="actions"><a class="button primary" href="${docs}deploy.md">${escape(c.primary)} ${arrow}</a><a class="button soft" href="${repository}">${escape(c.secondary)} ${arrow}</a></div>
          <ul class="facts">${c.facts.map((f) => `<li>${escape(f)}</li>`).join('')}</ul>
        </div>
        <figure class="plate">
          ${plate(escape(c.plateLabel))}
          <figcaption><span>${escape(c.plateCaption[0])}</span>${escape(c.plateCaption[1])}</figcaption>
        </figure>
      </section>
      <section class="run section" id="how-it-works" aria-labelledby="run-title">
        <div class="run-copy">
          <h2 id="run-title">${heading(c.runHeading)}</h2>
          <p>${escape(c.runText)}</p>
        </div>
        <figure class="record" aria-label="${escape(c.flowLabel)}">
          <div class="request"><span class="label">${escape(c.flowAgent)}</span><q>${escape(c.flowRequest)}</q></div>
          <div class="record-card">
            <div class="record-head"><span class="task">${escape(c.flowTask)}</span><span class="runtime">${escape(c.flowRuntime)}</span><span class="chip"><svg viewBox="0 0 16 16" aria-hidden="true"><path d="M3.5 8.5l3 3 6-7"/></svg>${escape(c.flowDone)}</span></div>
            <ol class="stages">${c.flowStages.map(([s, t]) => `<li><span class="node" aria-hidden="true"></span><span class="stage">${escape(s)}</span><span class="time">${escape(t)}</span></li>`).join('')}</ol>
          </div>
          <figcaption class="result"><span class="label">${escape(c.flowOutput)}</span><span class="file"><svg viewBox="0 0 20 20" aria-hidden="true"><path d="M5 2h6l4 4v12H5Z M11 2v5h4 M8 11h4m-4 3h4"/></svg><code>${escape(c.flowResult)}</code> + ${escape(c.flowLog)}</span></figcaption>
        </figure>
        <div class="features">${c.features.map(([title, text]) => `<article><h3>${escape(title)}</h3><p>${escape(text)}</p></article>`).join('')}</div>
      </section>
      <section class="connect section" id="connect" aria-labelledby="connect-title">
        <div class="connect-copy"><h2 id="connect-title">${heading(c.connectHeading)}</h2><p>${escape(c.connectText)}</p><a class="text-link" href="${docs}cli.md">${escape(c.guide)} ${arrow}</a></div>
        <div class="code-example" data-copy="${escape(c.copy)}" data-copied="${escape(c.copied)}" data-copy-error="${escape(c.copyError)}">
          <div class="tabs" role="tablist" aria-label="${escape(c.tabsLabel)}" hidden>${c.snippets.map((s, i) => `<button type="button" role="tab" id="tab-${s.id}" aria-controls="panel-${s.id}" aria-selected="${i === 0}" tabindex="${i === 0 ? '0' : '-1'}">${s.label}</button>`).join('')}</div>
          ${c.snippets.map((s) => `<section class="code-panel" id="panel-${s.id}" aria-labelledby="tab-${s.id}" tabindex="0"><div class="code-heading"><h3>${escape(s.title)}</h3><button class="copy" type="button" hidden>${escape(c.copy)}</button></div><pre><code>${escape(s.code)}</code></pre><p>${escape(s.note)}</p></section>`).join('')}
          <span class="copy-message" role="status" aria-live="polite"></span>
        </div>
      </section>
      <section class="trust section" aria-labelledby="trust-title"><h2 id="trust-title">${heading(c.trustHeading)}</h2><p class="trust-text">${escape(c.trustText)}</p><dl>${c.trustItems.map(([dt, dd]) => `<div><dt>${escape(dt)}</dt><dd>${escape(dd)}</dd></div>`).join('')}</dl></section>
      <section class="closing section" aria-labelledby="closing-title"><span class="dash" aria-hidden="true"></span><h2 id="closing-title">${heading(c.endHeading)}</h2><p>${escape(c.endText)}</p><a class="button primary" href="${docs}deploy.md">${escape(c.primary)} ${arrow}</a></section>
    </main>
    <footer><a class="family" href="https://niltavi.com">${escape(c.footerProduct)} ${arrow}</a><div><a href="${repository}">${escape(c.footerSource)}</a><a href="${repository}/blob/main/LICENSE">AGPL-3.0</a><a href="${base}assets/fonts/OFL.txt">DM Sans · OFL-1.1</a><a href="${base}assets/fonts/OFL-Cormorant.txt">Cormorant · OFL-1.1</a><span>© Niltavi</span></div></footer>
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
