// Woodcut-style scenes for the slides. Detail is carved out of flat shapes
// with tapered slivers that follow the form, the way a gouge cuts a block,
// instead of uniform strokes. Colors come from classes in site.css so both
// themes work under the style-src 'self' policy; motion is CSS keyframes and
// SMIL, both allowed by that policy.
import { readFileSync } from 'node:fs';

const r1 = (n) => Math.round(n * 10) / 10;

// Seeded so every build produces the same carving.
function random(seed) {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const lens = (t) => Math.sin(Math.PI * t) ** 0.85;
const blade = (t) => (1 - t) ** 0.9 * Math.min(1, t * 14 + 0.35);

// A filled cut along a polyline; profile(t) scales the half-width w.
function taper(points, w, profile = lens) {
  const left = [], right = [];
  points.forEach(([x, y], i) => {
    const [ax, ay] = points[Math.max(0, i - 1)];
    const [bx, by] = points[Math.min(points.length - 1, i + 1)];
    const len = Math.hypot(bx - ax, by - ay) || 1;
    const nx = -(by - ay) / len, ny = (bx - ax) / len;
    const h = w * profile(i / (points.length - 1));
    left.push(`${r1(x + nx * h)} ${r1(y + ny * h)}`);
    right.push(`${r1(x - nx * h)} ${r1(y - ny * h)}`);
  });
  return `M${left.join('L')}L${right.reverse().join('L')}Z`;
}

const sample = (fn, t0, t1, n = 14) => Array.from({ length: n }, (_, i) => fn(t0 + ((t1 - t0) * i) / (n - 1)));
const quad = ([x0, y0], [cx, cy], [x1, y1]) => (t) => [
  (1 - t) ** 2 * x0 + 2 * (1 - t) * t * cx + t * t * x1,
  (1 - t) ** 2 * y0 + 2 * (1 - t) * t * cy + t * t * y1,
];
const line = (points) => `M${points.map(([x, y]) => `${r1(x)} ${r1(y)}`).join('L')}`;

// A stream whose centerline repeats every `period` units, so its carved
// current can slide sideways forever without a seam.
function stream({ id, y, amp, thick, period = 400, seed, width = 800, from = 130, to = 670 }) {
  const mid = (x) => y + amp * Math.sin((2 * Math.PI * x) / period);
  const top = sample((x) => [x, mid(x) - thick / 2], -20, width + 20, 60);
  const bottom = sample((x) => [x, mid(x) + thick / 2 + 4 * Math.sin(x / 37)], width + 20, -20, 60);
  const band = line([...top, ...bottom]) + 'Z';
  const rand = random(seed);
  const cuts = [];
  for (let row = -thick / 2 + 7; row < thick / 2 - 5; row += 7 + rand() * 4) {
    for (let x = -rand() * 120; x < width; ) {
      const length = 50 + rand() * 120;
      const s = x, e = Math.min(x + length, width + 60);
      const pts = sample((t) => [t, mid(t) + row * (1 + 0.08 * Math.sin(t / 50))], s, e, 16);
      cuts.push(taper(pts, 1.1 + rand() * 1.6));
      x += length + 14 + rand() * 50;
    }
  }
  const current = cuts.join('');
  return {
    mid,
    band,
    svg: `<clipPath id="${id}-clip"><path d="${band}"/></clipPath>
    <path class="c-water" d="${band}"/>
    <g clip-path="url(#${id}-clip)"><g class="current"><path class="c-water-cut" d="${current}"/><path class="c-water-cut" transform="translate(${-width})" d="${current}"/></g></g>`,
    // The boat follows the centerline across the plate and fades at its rim.
    path: line(sample((x) => [x, mid(x)], from, to, 50)),
  };
}

// A leaf: flat body, carved midrib and paired side veins.
function leaf(from, ctrl, to, w, tone = 'c-leaf') {
  const curve = quad(from, ctrl, to);
  const body = taper(sample(curve, 0, 1, 18), w);
  const rib = taper(sample(curve, 0.06, 0.9, 14), w * 0.13);
  const veins = [0.28, 0.46, 0.64].flatMap((t) => {
    const [x, y] = curve(t), [x2, y2] = curve(t + 0.12);
    const dx = x2 - x, dy = y2 - y, len = Math.hypot(dx, dy), nx = -dy / len, ny = dx / len;
    return [1, -1].map((side) => taper([[x, y], [x + dx * 0.5 + nx * w * 0.42 * side, y + dy * 0.5 + ny * w * 0.42 * side], [x + dx * 0.9 + nx * w * 0.7 * side, y + dy * 0.9 + ny * w * 0.7 * side]], w * 0.07));
  });
  return `<path class="${tone}" d="${body}"/><path class="c-cut-soft" d="${rib}${veins.join('')}"/>`;
}

// A reed: a tapered blade from its base, swaying from that base.
function reed([x, y], height, lean, w, tone, delay) {
  const tip = [x + lean, y - height];
  const blade1 = taper(sample(quad([x, y], [x + lean * 0.15, y - height * 0.6], tip), 0, 1, 18), w, blade);
  const cut = taper(sample(quad([x, y - 6], [x + lean * 0.15, y - height * 0.6], tip), 0.08, 0.8, 14), w * 0.16);
  return `<g class="sway d${delay}"><path class="${tone}" d="${blade1}"/><path class="c-cut-soft" d="${cut}"/></g>`;
}

// A sun or moon disc with horizontal cuts across its lower half.
function disc(id, cx, cy, r, tone, cutTone, rows = 6) {
  const cuts = [];
  for (let i = 0; i < rows; i++) {
    const yy = cy + r * 0.18 + i * ((r * 0.8) / rows);
    const half = Math.sqrt(Math.max(0, r * r - (yy - cy) ** 2));
    cuts.push(taper(sample((x) => [x, yy], cx - half - 4, cx + half + 4, 10), 1.4 + i * 0.55));
  }
  return `<clipPath id="${id}"><circle cx="${cx}" cy="${cy}" r="${r}"/></clipPath><circle class="${tone}" cx="${cx}" cy="${cy}" r="${r}"/><g clip-path="url(#${id})"><path class="${cutTone}" d="${cuts.join('')}"/></g>`;
}

// A hill with grain cuts following its ridge.
function hill(points, tone, cutTone, seed, base = 600) {
  const rand = random(seed);
  const ridge = sample((t) => {
    const i = Math.min(points.length - 2, Math.floor(t * (points.length - 1)));
    const f = t * (points.length - 1) - i;
    const s = (1 - Math.cos(Math.PI * f)) / 2;
    return [points[i][0] + (points[i + 1][0] - points[i][0]) * f, points[i][1] + (points[i + 1][1] - points[i][1]) * s];
  }, 0, 1, 60);
  const shape = `${line(ridge)}L${ridge.at(-1)[0]} ${base}L${ridge[0][0]} ${base}Z`;
  const cuts = [];
  for (let k = 1; k < 5; k++) {
    const offset = 12 + k * 13;
    for (let t = rand() * 0.2; t < 0.95; t += 0.16 + rand() * 0.2) {
      const span = 0.08 + rand() * 0.14;
      const seg = ridge.slice(Math.floor(t * 59), Math.min(59, Math.floor((t + span) * 59)) + 1).map(([x, y]) => [x, y + offset]);
      if (seg.length > 2) cuts.push(taper(seg, 0.9 + rand() * 1.2));
    }
  }
  return `<path class="${tone}" d="${shape}"/><path class="${cutTone}" d="${cuts.join('')}"/>`;
}

function stone(cx, cy, rx, ry, tone = 'c-stone') {
  const body = `M${cx - rx} ${cy}C${cx - rx} ${cy - ry * 1.3} ${cx + rx * 0.9} ${cy - ry * 1.25} ${cx + rx} ${cy - ry * 0.1}C${cx + rx * 1.05} ${cy + ry * 0.8} ${cx - rx * 0.95} ${cy + ry * 0.9} ${cx - rx} ${cy}Z`;
  const shine = taper(sample(quad([cx - rx * 0.6, cy - ry * 0.35], [cx - rx * 0.1, cy - ry * 0.95], [cx + rx * 0.5, cy - ry * 0.6]), 0, 1, 12), ry * 0.12);
  const shine2 = taper(sample(quad([cx - rx * 0.4, cy - ry * 0.05], [cx, cy - ry * 0.5], [cx + rx * 0.3, cy - ry * 0.35]), 0, 1, 10), ry * 0.07);
  return `<path class="${tone}" d="${body}"/><path class="c-cut-soft" d="${shine}${shine2}"/>`;
}

// The task: a leaf boat carrying a folded note.
const boat = (lantern = false) => `<g class="boat">
  <path class="c-boat" d="${taper(sample((x) => [x, 4 + 0.006 * x * x], -30, 30, 16), 8, (t) => Math.sin(Math.PI * t) ** 0.6)}"/>
  <path class="c-cut" d="${taper(sample((x) => [x, 3 + 0.005 * x * x], -22, 22, 12), 1.1)}"/>
  <path class="c-note" d="M-11 -2L-3 -30L13 -4Z"/><path class="c-note-fold" d="M-3 -30L2 -3L13 -4Z"/>
  ${lantern ? '<circle class="c-glow" cx="-16" cy="-12" r="9"/><circle class="c-lantern" cx="-16" cy="-12" r="3.6"/>' : ''}
</g>`;

const voyage = (path, dur, lantern = false) => `<g class="voyage" opacity="0"><g transform="scale(1.6)">${boat(lantern)}</g><animateMotion dur="${dur}s" repeatCount="indefinite" rotate="auto" path="${path}"/><animate attributeName="opacity" values="0;1;1;0" keyTimes="0;0.1;0.9;1" dur="${dur}s" repeatCount="indefinite"/></g>`;

const birdSource = readFileSync(new URL('../art/niltavi-bird.svg', import.meta.url), 'utf8');
const birdInner = birdSource
  .replace(/^[\s\S]*?<\/desc>/, '')
  .replace(/<\/svg>\s*$/, '')
  .replace(/<!--[\s\S]*?-->/g, '')
  .replace(/ id="[^"]*"/g, '')
  .replace(/\n\s*/g, '');
// The vendored bird faces left and keeps its own colors in both themes.
const bird = (x, y, h, flip = false) => {
  const w = (h * 1030) / 1380;
  const flipped = flip ? ` transform="translate(${2 * x + w}) scale(-1 1)"` : '';
  return `<g${flipped}><svg x="${x}" y="${y}" width="${r1(w)}" height="${h}" viewBox="20 80 1030 1380" aria-hidden="true">${birdInner}</svg></g>`;
};

const sparkle = (x, y, s, delay, tone = 'c-spark') => `<path class="${tone} twinkle d${delay}" d="M${x} ${y - s}Q${x + s * 0.18} ${y - s * 0.18} ${x + s} ${y}Q${x + s * 0.18} ${y + s * 0.18} ${x} ${y + s}Q${x - s * 0.18} ${y + s * 0.18} ${x - s} ${y}Q${x - s * 0.18} ${y - s * 0.18} ${x} ${y - s}Z"/>`;

const frame = (name, label, body) => `<svg class="art art-${name}" viewBox="90 0 620 600" role="img" aria-label="${label}">${body}</svg>`;

// Each scene sits in a round plate, like the circle behind the niltavi.com
// bird; reeds, branches and the bird reach past its edge.
const plate = (id, cx, cy, r, tone, inner) => `<clipPath id="${id}-plate"><circle cx="${cx}" cy="${cy}" r="${r}"/></clipPath><circle class="${tone}" cx="${cx}" cy="${cy}" r="${r}"/><g clip-path="url(#${id}-plate)">${inner}</g>`;
const reeds = (list) => list.map(([x, y, h, l, w, tone], i) => reed([x, y], h, l, w, tone, i % 3)).join('');

// Cover: morning over the stream. The routine has been handed off.
export function cover(label) {
  const s = stream({ id: 'cv', y: 420, amp: 24, thick: 76, seed: 3 });
  return frame('cover', label, `
    ${plate('cv', 400, 300, 282, 'c-sky', `
      ${disc('cv-sun', 470, 262, 150, 'c-sun', 'c-sun-cut', 7)}
      ${hill([[100, 360], [230, 306], [380, 346], [560, 312], [720, 366]], 'c-hill', 'c-hill-cut', 11)}
      ${s.svg}
      ${hill([[100, 484], [260, 470], [440, 498], [620, 478], [720, 492]], 'c-bank', 'c-bank-cut', 7)}
      ${stone(300, 520, 34, 16)}${stone(350, 534, 20, 10)}${stone(540, 526, 28, 13)}`)}
    ${reeds([[188, 548, 300, -70, 10, 'c-leaf-deep'], [208, 560, 230, -20, 8, 'c-leaf'], [230, 572, 330, -110, 11, 'c-leaf-deep'], [580, 572, 260, 80, 10, 'c-leaf'], [606, 560, 320, 120, 11, 'c-leaf-deep'], [630, 546, 210, 40, 8, 'c-leaf-deep']])}
    ${sparkle(130, 130, 9, 0)}${sparkle(690, 90, 7, 1)}${sparkle(726, 196, 5, 2)}`);
}

// Night: the scheduled run sets off on its own while everyone sleeps.
export function night(label) {
  const s = stream({ id: 'nt', y: 440, amp: 22, thick: 72, seed: 21 });
  const stars = [[270, 150, 8], [350, 104, 5], [560, 130, 6], [620, 220, 4], [210, 250, 4], [450, 80, 4]]
    .map(([x, y, r], i) => sparkle(x, y, r, i % 3, 'c-star')).join('');
  return frame('night', label, `
    ${plate('nt', 400, 300, 282, 'c-night', `
      ${stars}
      <path class="c-moon" d="M520 130A62 62 0 1 0 584 228A50 50 0 1 1 520 130Z"/>
      <path class="c-moon-cut" d="${taper(sample(quad([500, 156], [478, 196], [512, 236]), 0, 1, 12), 2.4)}${taper(sample(quad([514, 172], [500, 198], [522, 222]), 0, 1, 10), 1.4)}"/>
      ${hill([[100, 400], [260, 346], [440, 384], [720, 336]], 'c-night-hill', 'c-night-cut', 5)}
      ${s.svg}
      ${voyage(s.path, 16, true)}
      ${hill([[100, 504], [240, 488], [440, 512], [720, 494]], 'c-night-hill', 'c-night-cut', 9)}`)}
    ${reeds([[176, 540, 280, -60, 10, 'c-leaf-deep'], [200, 556, 210, -10, 8, 'c-leaf-deep'], [600, 566, 300, 90, 10, 'c-leaf-deep'], [626, 548, 220, 30, 8, 'c-leaf-deep']])}`);
}

// Your own ground: the run passes its stages inside a ring of trees.
export function ground(label) {
  const s = stream({ id: 'gd', y: 390, amp: 26, thick: 80, seed: 33 });
  const tree = (x, y, r, tone) => {
    // Cuts fan out from where the trunk meets the crown, like leaf veins.
    const cuts = Array.from({ length: 7 }, (_, k) => {
      const a = Math.PI * (1.17 + k * 0.11);
      const [ox, oy] = [x, y + r * 0.62];
      return taper(sample(quad([ox, oy], [ox + Math.cos(a) * r * 0.5, oy + Math.sin(a) * r * 0.62], [ox + Math.cos(a) * r * 1.05, oy + Math.sin(a) * r * 1.25]), 0.18, 1, 12), r * 0.045, (t) => (1 - t) ** 0.6 * Math.min(1, t * 6));
    }).join('');
    const trunk = taper(sample(quad([x, y + r * 2.2], [x + 2, y + r], [x, y + r * 0.4]), 0, 1, 10), r * 0.12, blade);
    return `<path class="c-trunk" d="${trunk}"/><circle class="${tone}" cx="${x}" cy="${y}" r="${r}"/><path class="c-cut-soft" d="${cuts}"/>`;
  };
  const trees = [[150, 220, 56, 'c-leaf-deep'], [230, 150, 62, 'c-leaf'], [330, 112, 52, 'c-leaf-deep'], [470, 112, 52, 'c-leaf'], [570, 150, 62, 'c-leaf-deep'], [650, 220, 56, 'c-leaf']]
    .map(([x, y, r, tone]) => tree(x, y, r, tone)).join('');
  const stones = [240, 400, 560].map((x, i) => {
    const y = s.mid(x);
    return `<g class="stage d${i}"><ellipse class="ripple" cx="${x}" cy="${y + 4}" rx="44" ry="15"/><ellipse class="ripple late" cx="${x}" cy="${y + 4}" rx="44" ry="15"/>${stone(x, y + 2, 30, 14, 'c-stone-dark')}</g>`;
  }).join('');
  return frame('ground', label, `
    ${plate('gd', 400, 300, 282, 'c-clearing', `
      ${trees}
      ${s.svg}
      ${stones}
      ${voyage(s.path, 14)}
      ${hill([[100, 500], [300, 486], [520, 512], [720, 494]], 'c-bank', 'c-bank-cut', 13)}`)}
    ${reeds([[180, 544, 220, -60, 9, 'c-leaf'], [204, 558, 160, -10, 7, 'c-leaf-deep'], [612, 554, 240, 70, 9, 'c-leaf'], [636, 540, 170, 20, 7, 'c-leaf-deep']])}`);
}

// The keys: hanging from your own branch, watched by the bird.
export function keys(label) {
  const branch = taper(sample(quad([-20, 230], [300, 150], [820, 196]), 0, 1, 30), 9, (t) => 1 - t * 0.6);
  const branchCut = taper(sample(quad([40, 216], [300, 146], [700, 180]), 0, 1, 24), 1.6);
  const leaves = [
    leaf([180, 192], [200, 120], [250, 90], 16), leaf([196, 190], [150, 150], [110, 120], 13, 'c-leaf-deep'),
    leaf([620, 174], [650, 110], [700, 86], 15, 'c-leaf-deep'), leaf([640, 178], [700, 200], [760, 170], 13),
    leaf([420, 168], [440, 230], [420, 290], 12, 'c-leaf-deep'),
  ].join('');
  const ring = 'M0 0m-28 0a28 28 0 1 0 56 0a28 28 0 1 0 -56 0Zm11 0a17 17 0 1 1 34 0a17 17 0 1 1 -34 0Z';
  const key = `<g class="key"><path class="c-thread" d="M0 -136V-28"/>
    <path class="c-key" fill-rule="evenodd" d="${ring}"/><path class="c-key" d="M-6 24h12v120h-12Z M6 96h22v12H6Z M6 122h16v12H6Z"/>
    <path class="c-key-cut" d="${taper(sample((a) => [Math.cos(a) * 22.5, Math.sin(a) * 22.5], Math.PI * 1.1, Math.PI * 1.7, 12), 1.6)}${taper([[0, 34], [0, 80], [0, 132]], 1.5)}"/>
    ${sparkle(18, -14, 9, 0)}</g>`;
  return frame('keys', label, `
    ${plate('ky', 400, 330, 230, 'c-sky', `${disc('ky-sun', 400, 330, 170, 'c-sun', 'c-sun-cut', 6)}${hill([[160, 470], [320, 440], [500, 472], [660, 446]], 'c-hill', 'c-hill-cut', 17)}`)}
    <path class="c-branch" d="${branch}"/><path class="c-cut-soft" d="${branchCut}"/>
    ${leaves}
    <g transform="translate(330 312)">${key}</g>
    ${bird(470, 30, 190, true)}`);
}

// Morning: the night's run is on record, page after page.
export function record(label) {
  const rows = [0, 1, 2, 3, 4, 5].map((i) => `<path class="log d${i}" d="M0 ${i * 26}H${[150, 120, 160, 96, 140, 110][i]}"/>`).join('');
  const sprig = [leaf([96, 236], [70, 170], [96, 104], 20), leaf([96, 200], [140, 170], [150, 120], 14, 'c-leaf-deep'), leaf([96, 170], [56, 150], [40, 110], 12, 'c-leaf-deep')].join('');
  return frame('record', label, `
    ${plate('rc', 470, 300, 260, 'c-sky', `${disc('rc-sun', 520, 230, 130, 'c-sun', 'c-sun-cut', 6)}${hill([[200, 430], [360, 392], [560, 430], [740, 396]], 'c-hill', 'c-hill-cut', 23)}${hill([[200, 500], [400, 480], [600, 506], [740, 486]], 'c-bank', 'c-bank-cut', 29)}`)}
    <g transform="translate(170 150) rotate(-4)">
      <path class="c-card-shadow" d="M8 12h420v300H8Z"/>
      <path class="c-card" d="M0 0h420v300H0Z"/>
      <path class="c-card-rule" d="M24 26h372M24 276h372M196 48v206"/>
      <path class="c-leaf-deep" d="${taper([[96, 252], [96, 160], [96, 70]], 1.6)}"/>
      ${sprig}
      <g transform="translate(224 70)">${rows}</g>
      <g class="stamp" transform="translate(356 238)"><circle class="c-stamp" r="24"/><path class="c-stamp-cut" d="M-10 1l7 7 13-15"/></g>
    </g>
    ${sparkle(120, 120, 8, 0)}${sparkle(690, 380, 6, 1)}`);
}
