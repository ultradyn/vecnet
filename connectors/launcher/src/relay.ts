import type { Attachment } from '../../hub-client/src/wire.ts';
import type { JobRow } from './ledger.ts';

export type RelayRole = 'requester' | 'operator' | 'relay_from';
export type RelayDecision = { relay: true; role: RelayRole } | { relay: false; note: 'not_relayed' | 'self' };
export function relayDecision(sender: string, job: Pick<JobRow, 'requester' | 'input'>, self: string, isOperator: (id: string) => boolean): RelayDecision {
  if (sender === self) return { relay: false, note: 'self' };
  if (isOperator(sender)) return { relay: true, role: 'operator' };
  if (sender === job.requester) return { relay: true, role: 'requester' };
  if (job.input.relay_from?.includes(sender)) return { relay: true, role: 'relay_from' };
  return { relay: false, note: 'not_relayed' };
}
// Strip line separators, terminal controls and directional overrides from labels.
const label = (s: string) => s.replace(/[\p{Cc}\p{Cf}\p{Zl}\p{Zp}]/gu, '');
export function formatRelayed(item: { role: RelayRole; name: string; peerId: string; messageId: string; text: string; attachments: Attachment[] }): string {
  const from = item.role === 'operator' ? 'From: operator' : 'from';
  const header = `[vecnet] ${from} ${label(item.name)} (${item.peerId}), ${item.messageId}:`;
  const text = item.text.replace(/\r\n?/g, '\n').split('\n').map(line => `> ${line}`).join('\n');
  const attachments = item.attachments.length ? `\n[attachments not forwarded: ${item.attachments.map(a => `${label(a.name ?? 'unnamed')} (${a.sha256.slice(0, 12)})`).join(', ')}]` : '';
  return `${header}\n${text}${attachments}`;
}
