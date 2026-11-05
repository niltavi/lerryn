# Lerryn introduction website

English and Simplified Chinese pages for `https://lerryn.niltavi.com`. This is a standalone static website; the self-hosted application keeps its own web console in `apps/web/`.

```sh
pnpm website:build
pnpm website:check
```

The build writes `website/dist/`. Open its `index.html` directly for a local review, or serve that directory with your preferred local server. The Chinese page is at `zh/index.html`. Developers manage their own preview servers.

## Sources

- `content.mjs`: both languages, the agent prompt, documentation language per page, and canonical domain. The English page links `docs/en/`; the Chinese page links the Chinese documents in `docs/`.
- `llms.txt`: installation steps for agents. The site's prompt sends an agent to lerryn.niltavi.com, and this file tells it how to deploy and connect Lerryn using the English guides on GitHub.
- `scripts/build.mjs`: semantic HTML, metadata, sitemap, and security headers.
- `assets/site.css`: responsive layout on the Niltavi UI palette (`design/niltavi-ui/tokens.json`), Cormorant Garamond headings, and DM Sans text.
- `scripts/art.mjs`: the woodcut-style scenes, one per slide. Shapes and tapered carving cuts are generated with a fixed seed; colors come from CSS classes so they follow the theme, and motion uses CSS keyframes and SMIL, which pause off screen and under reduced motion.
- `art/niltavi-bird.svg`: unmodified niltavi bird (the build drops its own twig and turns it so both feet rest on the scene's branch) from [niltavi/niltavi-ui](https://github.com/niltavi/niltavi-ui/blob/ae424e237224ee54f9e7d2a76522c1117fecc51b/docs/assets/niltavi-bird.svg) (MIT, same license as `design/niltavi-ui/LICENSE`).
- `art/niltavi-wordmark.svg`: unmodified muted-light wordmark from [niltavi/niltavi-logo](https://github.com/niltavi/niltavi-logo/blob/00042d2ffc7b3746bd6976937098d057fcd8c84e/svg/niltavi-muted-light.svg) (outlined DM Sans, OFL). The build inlines it in the footer and maps its colors to theme tokens.
- `assets/site.js`: optional theme selection, scene playback, and the prompt's copy button. All content stays available without JavaScript.
- `assets/paging.js`: one slide per wheel/trackpad gesture on desktop, with arrow, Page Up/Down, Space, and Home/End navigation. Momentum cannot skip slides. Narrow screens and layouts with oversized slides use normal scrolling; reduced motion switches pages instantly. The footer remains a separate scroll stop.
- `assets/fonts/`: DM Sans 5.3.0 files already used by the application and Cormorant Garamond 5.3.0, each with its original OFL license.

The build copies the current logo from `design/brand/logo/` and inlines `lerryn-mark.svg` in the header with theme colors, so dark pages need no light plate; do not edit a separate logo copy. Fonts and other assets are served locally. The pages have no analytics, account forms, runtime API calls, or framework dependencies.

The favicon uses `lerryn-icon-circle.svg`: the original wave on a warm-paper circle with a fine border, following niltavi.com. A matching 32px PNG provides a fallback, and a 180px PNG serves as the Apple touch icon. Run `pnpm design:logo` to regenerate these assets from the logo master before building the website.

## Hosting

Configure a static host with the repository root as its working directory, Node.js 24, build command `pnpm website:build`, and output directory `website/dist`. The build itself uses only Node built-ins and does not require dependency installation. `_headers` applies the supplied security policy on hosts that support the format, including Cloudflare Pages; use equivalent host settings elsewhere. Preserve `/zh/` as a directory route.

Attach `lerryn.niltavi.com` in the chosen host and provision DNS and TLS when publishing. This repository does not create DNS records or deploy the site automatically. Both canonical URLs and the sitemap already use the intended public domain.

The source repository currently follows the project's private visibility. Before a public launch, make sure the GitHub documentation and source links are accessible to the intended visitors. Container release publication and website hosting are separate operations.
