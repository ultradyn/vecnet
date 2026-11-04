import { randomBytes } from 'node:crypto';

const ulid = '[0-9A-HJKMNP-TV-Z]{26}';
export const ID = {
  peer: new RegExp(`^peer_${ulid}$`),
  team: new RegExp(`^(team|grp)_${ulid}$`),
  grp: new RegExp(`^(team|grp)_${ulid}$`),
  thread: new RegExp(`^thr_${ulid}$`),
  message: new RegExp(`^msg_${ulid}$`),
  delivery: new RegExp(`^dly_${ulid}$`),
  source: new RegExp(`^src_${ulid}$`),
  approval: new RegExp(`^apr_${ulid}$`),
} as const;

const alphabet = '0123456789ABCDEFGHJKMNPQRSTVWXYZ';
export function newUlid(nowMs: number): string {
  if (!Number.isSafeInteger(nowMs) || nowMs < 0 || nowMs > 0xffffffffffff) throw new RangeError('invalid ULID time');
  let bits = BigInt(nowMs);
  for (const byte of randomBytes(10)) bits = (bits << 8n) | BigInt(byte);
  let result = '';
  for (let n = 0; n < 26; n++) { result = alphabet[Number(bits & 31n)] + result; bits >>= 5n; }
  return result;
}
export function newApprovalId(nowMs: number): string { return `apr_${newUlid(nowMs)}`; }
