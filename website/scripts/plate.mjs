// The hero plate: a stream between reeds and stones, drawn as flat shapes with
// a few carved lines. Colors come from classes in site.css so both themes work
// under the style-src 'self' policy.
const leaf = (x, y, angle, scale, tone) => `<g transform="translate(${x} ${y}) rotate(${angle}) scale(${scale})"><path class="${tone}" d="M0 0C18-34 52-62 104-70C82-34 50-8 0 0Z"/><path class="rib" d="M6-3Q50-34 96-64"/><path class="rib thin" d="M38-22l10-14M60-38l12-12M30-17l-10-6M54-32l-12-4"/></g>`;

export const plate = (label) => `<svg class="plate-art" viewBox="0 0 600 560" role="img" aria-label="${label}">
  <defs><clipPath id="sun-clip"><circle cx="352" cy="228" r="176"/></clipPath></defs>
  <circle class="sun" cx="352" cy="228" r="176"/>
  <g class="sun-lines" clip-path="url(#sun-clip)"><path d="M150 300H560M150 318H560M150 336H560M150 354H560M150 372H560"/></g>
  <path class="bank" d="M-10 430C120 404 220 420 330 446S520 470 610 452V512C470 552 160 556-10 528Z"/>
  <path class="water" d="M-10 372C90 312 190 312 290 372S490 432 610 372V446C490 506 390 506 290 446S90 386-10 446Z"/>
  <g class="water-lines">
    <path d="M-10 390C90 330 190 330 290 390S490 450 610 390" stroke-dasharray="70 12 120 16 60 10 200"/>
    <path d="M30 404C110 362 190 360 290 410S470 456 610 408" stroke-dasharray="40 18 90 12 150 22 80"/>
    <path d="M-10 424C80 380 180 382 290 428S500 474 610 426" stroke-dasharray="110 14 50 20 130 12 60"/>
  </g>
  <g class="stone"><path d="M150 506C158 488 196 484 212 498C222 508 210 520 182 520C160 520 146 516 150 506Z"/><path d="M402 512C406 496 438 490 456 500C468 508 462 522 436 524C414 525 400 522 402 512Z"/><path d="M470 520C474 510 494 508 504 514C510 520 506 528 490 528C478 528 468 526 470 520Z"/></g>
  <g class="stone-lines"><path d="M164 502Q180 494 198 498M418 506Q432 498 448 504"/></g>
  <path class="stem" d="M128 532C132 450 150 360 196 262M84 528C86 470 94 410 112 360M548 520C552 470 560 420 586 360"/>
  ${leaf(196, 262, -6, 1, 'leaf')}
  ${leaf(170, 330, 200, 0.9, 'leaf deep')}
  ${leaf(142, 410, -24, 1.05, 'leaf')}
  ${leaf(112, 360, 236, 0.8, 'leaf deep')}
  ${leaf(132, 470, 196, 0.85, 'leaf')}
  ${leaf(586, 360, 210, 1.1, 'leaf deep')}
  ${leaf(560, 432, 168, 0.95, 'leaf')}
  ${leaf(551, 486, 214, 0.8, 'leaf deep')}
  <g class="marks"><path d="M108 96v14M101 103h14M520 58v12M514 64h12"/><path d="M244 44h28"/></g>
</svg>`;
