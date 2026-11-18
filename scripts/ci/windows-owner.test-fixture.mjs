// Non-executable fixture: an independent DOS + COFF header is enough to test
// artifact validation without compiling or executing a Windows image on Linux.
import { createHash } from 'node:crypto';
import { writeFile } from 'node:fs/promises';
import path from 'node:path';

export async function ownerFixture(directory, platform, file = 'ci-windows-owner.exe') {
  const bytes = Buffer.alloc(512);
  bytes.write('MZ');
  bytes.writeUInt32LE(128, 0x3c);
  bytes.write('PE\0\0', 128);
  bytes.writeUInt16LE(platform === 'win32-arm64' ? 0xaa64 : 0x8664, 132);
  await writeFile(path.join(directory, file), bytes);
  return {ciWindowsOwner: {file, sha256: createHash('sha256').update(bytes).digest('hex')}};
}
