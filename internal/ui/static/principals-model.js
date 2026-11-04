// Pure rows for the dashboard's Principals card (PROTOCOL §18.4). No DOM.
import { clean } from './format.js';

const n = v => (Number.isFinite(v) ? v : 0);
const list = v => (Array.isArray(v) ? v.filter(s => typeof s === 'string') : []);

// principalRows keeps principals the operator limited or that created peers,
// sorted by name. scopes "none" with other peers scoped is the §18.4 warning.
export function principalRows(principals) {
  return (Array.isArray(principals) ? principals : [])
    .filter(p => p && (n(p.max_peers) > 0 || n(p.active) + n(p.disabled) > 0))
    .map(p => {
      const allow = list(p.default_allow).length, deny = list(p.default_deny).length;
      const scopes = allow + deny ? `allow ${allow} · deny ${deny}` : 'none';
      const active = n(p.active), max = n(p.max_peers);
      return {
        id: String(p.principal_id || ''), name: clean(p.name || ''), active, max, disabled: n(p.disabled),
        createsLastHour: n(p.creates_last_hour), createsPerHour: n(p.creates_per_hour), kinds: list(p.kinds),
        scopes, unscopedRisk: scopes === 'none' && n(p.scoped_peers) > 0, full: active >= max,
      };
    })
    .sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : a.id < b.id ? -1 : a.id > b.id ? 1 : 0));
}
