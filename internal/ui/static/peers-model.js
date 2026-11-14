// Shared dashboard/browse visibility; archived peers are a separate lifecycle state.
import { peerRows } from './dash-model.js';
const KEY = 'vecnet.ui.peer-filters';
export function readPeerFilters(storage) {
 try { const v = JSON.parse(storage?.getItem(KEY) || '{}'); return { showDisabled: v.showDisabled === true, showArchived: v.showArchived === true }; } catch { return { showDisabled: false, showArchived: false }; }
}
export function savePeerFilters(storage, value) { try { storage?.setItem(KEY, JSON.stringify({ showDisabled: !!value.showDisabled, showArchived: !!value.showArchived })); } catch {} }
export function filterPeers(rows, { showDisabled = false, showArchived = false, q = '', kind = '', presence = '', sort = 'recent', limit = Infinity, now = Date.now() } = {}) {
 const all = peerRows(rows), needle = q.trim().toLowerCase();
 const shown = all.filter(p => (p.archivedAt ? showArchived : !p.disabled || showDisabled)
  && (!needle || (p.name + ' ' + p.id).toLowerCase().includes(needle)) && (!kind || p.kind === kind) && (!presence || p.presence === presence));
 const recent = p => { const seen = Date.parse(p.seen); if (Number.isFinite(seen)) return seen; const presence = Date.parse(p.presenceAt); return Number.isFinite(presence) && presence <= now ? presence : 0; };
 shown.sort((a, b) => (sort === 'recent' ? recent(b) - recent(a) : 0) || a.name.localeCompare(b.name) || a.id.localeCompare(b.id));
 return { shown: shown.slice(0, limit), matched: shown.length, total: all.length, archived: all.filter(p => p.archivedAt).length, disabled: all.filter(p => p.disabled && !p.archivedAt).length };
}
