// Apply an explicit choice before the first paint, including a direct locale URL.
try {
  const theme = localStorage.getItem('lerryn-site-theme');
  if (theme === 'light' || theme === 'dark') document.documentElement.dataset.theme = theme;
} catch {
  // Without storage, CSS follows the system preference.
}
