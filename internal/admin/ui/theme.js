// Applies the saved or system colour scheme before first paint (no flash).
(function () {
  var t = null;
  try { t = localStorage.getItem('xl-theme'); } catch (e) { /* storage may be blocked */ }
  if (!t) t = matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  document.documentElement.setAttribute('data-bs-theme', t);
})();
