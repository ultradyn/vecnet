// Appearance: theme and density for this browser only (plan Decision 6).
// They are not hub settings, API values or flags, and nothing here makes a
// request. The stored object holds only two validated enum scalars under a
// versioned key; blocked or corrupt storage falls back to the defaults.
// appearance-boot.js applies the same attributes before first paint.

export const APPEARANCE_KEY = 'vecnet.ui.appearance.v1';
export const THEMES = ['system', 'dark', 'light'];
export const DENSITIES = ['comfortable', 'compact'];
export const DEFAULT_APPEARANCE = Object.freeze({ theme: 'system', density: 'comfortable' });

// normalizeAppearance keeps only known enum values, field by field.
export function normalizeAppearance(v) {
  const o = v && typeof v === 'object' && !Array.isArray(v) ? v : {};
  return {
    theme: THEMES.includes(o.theme) ? o.theme : DEFAULT_APPEARANCE.theme,
    density: DENSITIES.includes(o.density) ? o.density : DEFAULT_APPEARANCE.density,
  };
}
export function parseAppearance(raw) {
  if (typeof raw !== 'string') return { ...DEFAULT_APPEARANCE };
  try { return normalizeAppearance(JSON.parse(raw)); } catch { return { ...DEFAULT_APPEARANCE }; }
}
export function readAppearance(storage) {
  try { return parseAppearance(storage ? storage.getItem(APPEARANCE_KEY) : null); } catch { return { ...DEFAULT_APPEARANCE }; }
}
// writeAppearance: false when the browser blocks storage.
export function writeAppearance(storage, a) {
  const v = normalizeAppearance(a);
  try {
    if (!storage) return false;
    storage.setItem(APPEARANCE_KEY, JSON.stringify({ theme: v.theme, density: v.density }));
    return true;
  } catch { return false; }
}

const LIGHT = '(prefers-color-scheme: light)';
// resolveTheme: `system` follows the browser's colour scheme.
export function resolveTheme(theme, matchMedia) {
  if (theme === 'light' || theme === 'dark') return theme;
  try { return matchMedia && matchMedia(LIGHT).matches ? 'light' : 'dark'; } catch { return 'dark'; }
}
export function applyAppearance(root, a, matchMedia) {
  const v = normalizeAppearance(a);
  root.setAttribute('data-theme', resolveTheme(v.theme, matchMedia));
  root.setAttribute('data-theme-pref', v.theme);
  root.setAttribute('data-density', v.density);
  return v;
}

// createAppearance owns the live look: it applies the stored choice, follows
// the system scheme and other tabs (storage events), and set() writes a new
// choice. Subscribers hear every change, from this tab or another.
export function createAppearance({ storage, root, win } = {}) {
  const mm = win && typeof win.matchMedia === 'function' ? q => win.matchMedia(q) : null;
  const subs = new Set();
  let cur = readAppearance(storage);
  if (root) applyAppearance(root, cur, mm);
  const tell = () => { for (const fn of [...subs]) fn({ ...cur }); };
  const onStorage = e => {
    if (e && e.key !== null && e.key !== APPEARANCE_KEY) return;
    const next = readAppearance(storage);
    if (next.theme === cur.theme && next.density === cur.density) return;
    cur = next;
    if (root) applyAppearance(root, cur, mm);
    tell();
  };
  const media = mm ? mm(LIGHT) : null;
  const onScheme = () => { if (cur.theme === 'system' && root) applyAppearance(root, cur, mm); };
  if (win && win.addEventListener) win.addEventListener('storage', onStorage);
  if (media && media.addEventListener) media.addEventListener('change', onScheme);
  return {
    get: () => ({ ...cur }),
    // set applies a validated choice now; persisted is false when storage is
    // blocked, so the choice lasts only until reload.
    set(a) {
      cur = normalizeAppearance(a);
      const persisted = writeAppearance(storage, cur);
      if (root) applyAppearance(root, cur, mm);
      tell();
      return { persisted };
    },
    subscribe(fn) { subs.add(fn); return () => subs.delete(fn); },
    close() {
      subs.clear();
      if (win && win.removeEventListener) win.removeEventListener('storage', onStorage);
      if (media && media.removeEventListener) media.removeEventListener('change', onScheme);
    },
  };
}
