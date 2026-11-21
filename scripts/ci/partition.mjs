// Stable, exhaustive round-robin groups. Adding tests cannot strand them in an
// unmaintained allowlist. Indices are zero-based at this internal boundary.
export function partition(items, index, count) {
  if (!Number.isInteger(index) || !Number.isInteger(count) || count < 1 || index < 0 || index >= count) throw new Error('invalid partition');
  if (new Set(items).size !== items.length) throw new Error('duplicate inventory');
  return [...items].sort().filter((_, ordinal) => ordinal % count === index);
}

export function goInventory(output) {
  const result = [];
  let names = [];
  for (const line of output.split(/\r?\n/)) {
    if (/^(?:Test\w+|Example\w*|Fuzz\w+)$/.test(line)) { names.push(line); continue; }
    const match = /^(?:ok|\?)\s+(\S+)\s+/.exec(line);
    if (match) { result.push({ pkg: match[1], names }); names = []; }
    else if (line.trim()) throw new Error(`unrecognized Go inventory line: ${line}`);
  }
  if (names.length || !result.length || new Set(result.map(row => row.pkg)).size !== result.length) throw new Error('incomplete Go inventory');
  return result;
}
