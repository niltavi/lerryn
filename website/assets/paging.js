(() => {
  const root = document.documentElement;
  const slides = [...document.querySelectorAll('.slide')];
  const footer = document.querySelector('footer');
  const still = window.matchMedia('(prefers-reduced-motion: reduce)');
  let enabled = false;
  let distance = 0;
  let lastWheel = -Infinity;
  let usedGesture = false;
  let movingUntil = 0;

  // Long slides and stacked mobile layouts need uninterrupted reading space.
  const measure = () => {
    enabled = window.innerWidth > 900 && slides.length > 1 && slides.every((slide) => slide.getBoundingClientRect().height <= window.innerHeight + 2);
    root.classList.toggle('deck', enabled);
    distance = 0;
    usedGesture = false;
    lastWheel = -Infinity;
    movingUntil = 0;
  };
  measure();
  window.addEventListener('resize', measure);
  if ('ResizeObserver' in window) {
    const observer = new ResizeObserver(measure);
    slides.forEach((slide) => observer.observe(slide));
  }

  const positions = () => {
    const limit = Math.max(0, root.scrollHeight - window.innerHeight);
    return [...slides, footer].filter(Boolean).map((element) => Math.min(limit, element.getBoundingClientRect().top + window.scrollY));
  };
  const move = (direction, end = false) => {
    const stops = positions();
    const target = end ? (direction > 0 ? stops.at(-1) : stops[0]) : direction > 0
      ? stops.find((top) => top > window.scrollY + 2)
      : stops.findLast((top) => top < window.scrollY - 2);
    if (target === undefined || Math.abs(target - window.scrollY) <= 2) return;
    // scrollend normally releases this guard; the deadline covers older browsers.
    movingUntil = still.matches ? 0 : performance.now() + 700;
    window.scrollTo({ top: target, behavior: still.matches ? 'instant' : 'smooth' });
  };
  document.addEventListener('scrollend', () => { movingUntil = 0; });

  window.addEventListener('wheel', (event) => {
    if (!enabled || event.defaultPrevented || event.ctrlKey || event.metaKey || Math.abs(event.deltaX) >= Math.abs(event.deltaY)) return;
    event.preventDefault();
    const now = performance.now();
    // A trackpad's momentum belongs to the same gesture, even after landing.
    if (now - lastWheel > 180) {
      distance = 0;
      usedGesture = false;
    }
    lastWheel = now;
    if (usedGesture || now < movingUntil) return;
    const unit = event.deltaMode === 1 ? 16 : event.deltaMode === 2 ? window.innerHeight : 1;
    const delta = event.deltaY * unit;
    if (Math.sign(delta) !== Math.sign(distance)) distance = 0;
    distance += delta;
    if (Math.abs(distance) < 24) return;
    usedGesture = true;
    move(Math.sign(distance));
  }, { passive: false });

  window.addEventListener('keydown', (event) => {
    if (!enabled || event.defaultPrevented || event.altKey || event.ctrlKey || event.metaKey) return;
    if (event.target.closest('a, button, input, textarea, select, [contenteditable]:not([contenteditable="false"]), [role="textbox"]')) return;
    const direction = { ArrowDown: 1, PageDown: 1, ' ': event.shiftKey ? -1 : 1, ArrowUp: -1, PageUp: -1, Home: -1, End: 1 }[event.key];
    if (!direction) return;
    event.preventDefault();
    if (event.repeat && performance.now() < movingUntil) return;
    move(direction, event.key === 'Home' || event.key === 'End');
  });
})();
