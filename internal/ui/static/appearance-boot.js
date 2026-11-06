// A blocking classic script in <head>: it sets this browser's theme and
// density on <html> before first paint, so a light or compact choice never
// flashes dark or roomy while app.js loads. It reads the same key and enums
// as appearance.js, which takes over once the app runs. CSP allows 'self'.
(function () {
  var d = document.documentElement, theme = 'system', density = 'comfortable';
  try {
    var v = JSON.parse(localStorage.getItem('vecnet.ui.appearance.v1') || 'null');
    if (v && (v.theme === 'dark' || v.theme === 'light' || v.theme === 'system')) theme = v.theme;
    if (v && (v.density === 'comfortable' || v.density === 'compact')) density = v.density;
  } catch (e) { /* blocked or corrupt storage: the defaults */ }
  var light = theme === 'light';
  try { if (theme === 'system') light = !!(window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches); } catch (e) { light = false; }
  d.setAttribute('data-theme', light ? 'light' : 'dark');
  d.setAttribute('data-theme-pref', theme);
  d.setAttribute('data-density', density);
})();
