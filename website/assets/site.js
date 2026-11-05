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

  const example = document.querySelector('.code-example');
  const tablist = example.querySelector('[role="tablist"]');
  const tabs = [...tablist.querySelectorAll('[role="tab"]')];
  const panels = [...example.querySelectorAll('.code-panel')];
  function activate(tab, focus = false) {
    tabs.forEach((item) => {
      const active = item === tab;
      item.setAttribute('aria-selected', String(active));
      item.tabIndex = active ? 0 : -1;
    });
    panels.forEach((panel) => { panel.hidden = panel.id !== tab.getAttribute('aria-controls'); });
    example.querySelector('.copy-message').textContent = '';
    if (focus) tab.focus();
  }
  panels.forEach((panel) => panel.setAttribute('role', 'tabpanel'));
  activate(tabs[0]);
  tablist.hidden = false;
  tabs.forEach((tab, index) => {
    tab.addEventListener('click', () => activate(tab));
    tab.addEventListener('keydown', (event) => {
      let next;
      if (event.key === 'ArrowRight') next = (index + 1) % tabs.length;
      if (event.key === 'ArrowLeft') next = (index + tabs.length - 1) % tabs.length;
      if (event.key === 'Home') next = 0;
      if (event.key === 'End') next = tabs.length - 1;
      if (next !== undefined) { event.preventDefault(); activate(tabs[next], true); }
    });
  });
  example.querySelectorAll('.copy').forEach((button) => {
    button.hidden = false;
    button.addEventListener('click', async () => {
      const text = button.closest('.code-panel').querySelector('code').textContent;
      const status = example.querySelector('.copy-message');
      try {
        await navigator.clipboard.writeText(text);
        status.textContent = example.dataset.copied;
      } catch {
        const range = document.createRange();
        range.selectNodeContents(button.closest('.code-panel').querySelector('code'));
        const selection = window.getSelection();
        selection.removeAllRanges();
        selection.addRange(range);
        status.textContent = example.dataset.copyError;
      }
    });
  });
})();
