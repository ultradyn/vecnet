/** Admission is provisional: successful initialization is not proof that every
 * delivery operation works. Keep runtime shape, policy and receipt checks. */
export const CODEX_MIN_APPSERVER_VERSION = '0.166.0';
/** Historical protocol/model-context verification, not an admission allowlist. */
export const CODEX_VERIFIED_APPSERVER_VERSIONS: readonly string[] = Object.freeze(['0.166.0', '0.167.0', '0.167.2']);

const numeric = '(?:0|[1-9][0-9]*)';
const prerelease = `(?:${numeric}|[0-9]*[A-Za-z-][0-9A-Za-z-]*)`;
const semver = new RegExp(`^(${numeric})\\.(${numeric})\\.(${numeric})(?:-(${prerelease}(?:\\.${prerelease})*))?(?:\\+[0-9A-Za-z-]+(?:\\.[0-9A-Za-z-]+)*)?$`);

export function codexVersionCompatibility(version: string): 'unsupported' | 'verified' | 'tentative' {
  const parts = semver.exec(version);
  if (!parts) return 'unsupported';
  // Compare integer components without floating-point rounding or overflow.
  const floor = CODEX_MIN_APPSERVER_VERSION.split('.').map(BigInt);
  const core = parts.slice(1, 4).map(BigInt);
  let order = 0;
  for (let i = 0; i < 3; i++) {
    if (core[i] !== floor[i]) { order = core[i] > floor[i] ? 1 : -1; break; }
  }
  if (order < 0 || (order === 0 && parts[4] !== undefined)) return 'unsupported';
  return CODEX_VERIFIED_APPSERVER_VERSIONS.includes(version) ? 'verified' : 'tentative';
}

export function isCodexVersionSupported(version: string): boolean {
  return codexVersionCompatibility(version) !== 'unsupported';
}

export function parseServerVersion(userAgent: string): string | null {
  const version = /^[^/\s]+\/([^\s]+)(?:\s|$)/.exec(userAgent)?.[1];
  return version !== undefined && semver.test(version) ? version : null;
}
