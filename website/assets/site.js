(() => {
  const root = document.documentElement;
  // Scenes animate only while on screen, and not at all for reduced motion.
  root.classList.add('js');
  const still = window.matchMedia('(prefers-reduced-motion: reduce)');
  const scenes = [...document.querySelectorAll('.slide')];
  const pauseSmil = () => document.querySelectorAll('svg.art').forEach((svg) => (still.matches ? svg.pauseAnimations() : svg.unpauseAnimations()));
  pauseSmil();
  still.addEventListener('change', pauseSmil);
  if ('IntersectionObserver' in window) {
    const observer = new IntersectionObserver((entries) => entries.forEach((entry) => entry.target.classList.toggle('in-view', entry.isIntersecting)), { threshold: 0.25 });
    scenes.forEach((scene) => observer.observe(scene));
  } else {
    scenes.forEach((scene) => scene.classList.add('in-view'));
  }

  const toggle = document.querySelector('.theme-switch');
  const systemTheme = window.matchMedia('(prefers-color-scheme: dark)');
  let chosen;
  try { chosen = localStorage.getItem('lerryn-site-theme'); } catch { /* Storage can be disabled. */ }
  const setTheme = (theme) => {
    root.dataset.theme = theme;
    const dark = theme === 'dark';
    toggle.setAttribute('aria-pressed', String(dark));
    const label = dark ? document.body.dataset.themeLight : document.body.dataset.themeDark;
    toggle.setAttribute('aria-label', label);
    toggle.title = label;
  };
  setTheme(chosen === 'light' || chosen === 'dark' ? chosen : systemTheme.matches ? 'dark' : 'light');
  toggle.hidden = false;
  toggle.addEventListener('click', () => {
    chosen = root.dataset.theme === 'dark' ? 'light' : 'dark';
    setTheme(chosen);
    try { localStorage.setItem('lerryn-site-theme', chosen); } catch { /* Theme still works without persistence. */ }
  });
  systemTheme.addEventListener('change', (event) => { if (!chosen) setTheme(event.matches ? 'dark' : 'light'); });

  // The agent prompt is plain text on the page; copying is a convenience.
  const prompt = document.querySelector('.prompt');
  const button = prompt.querySelector('.copy');
  const text = prompt.querySelector('.prompt-text');
  const status = prompt.querySelector('.copy-message');
  button.hidden = false;
  button.addEventListener('click', async () => {
    try {
      await navigator.clipboard.writeText(text.textContent.trim());
      status.textContent = prompt.dataset.copied;
    } catch {
      const range = document.createRange();
      range.selectNodeContents(text);
      const selection = window.getSelection();
      selection.removeAllRanges();
      selection.addRange(range);
      status.textContent = prompt.dataset.copyError;
    }
  });
})();
