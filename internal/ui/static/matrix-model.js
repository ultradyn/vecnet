// The delivery matrix as rows of cells, one per recipient column. Pure: no DOM,
// no clock. The hub's `display` already overlays read on accepted and pushed
// (PROTOCOL §2.13), so the model never decides a colour itself.

// grid lines each row's cells up with `recipients`. A recipient with no
// delivery gets { state: 'none' }, or { state: 'self' } when it sent the
// message. A hub-authored message has no cells and still makes a row.
export function grid(matrix) {
  const recipients = (matrix && matrix.recipients) || [];
  return ((matrix && matrix.rows) || []).map(row => {
    const byRecipient = new Map((row.cells || []).map(c => [c.recipient, c]));
    return {
      message_id: row.message_id,
      created_at: row.created_at,
      sender: row.sender,
      excerpt: row.excerpt,
      cells: recipients.map(r => {
        const c = byRecipient.get(r);
        if (c) return { state: c.display || c.state, cell: c };
        return r === row.sender ? { state: 'self' } : { state: 'none' };
      }),
    };
  });
}

// merge appends page `next` to the matrix `acc` built so far. Recipients are a
// union in first-seen order; a message on both pages keeps the newer copy.
export function merge(acc, next) {
  const recipients = [...((acc && acc.recipients) || [])];
  for (const r of next.recipients || []) if (!recipients.includes(r)) recipients.push(r);
  const rows = [...((acc && acc.rows) || [])];
  const at = new Map(rows.map((r, i) => [r.message_id, i]));
  for (const row of next.rows || []) {
    if (at.has(row.message_id)) rows[at.get(row.message_id)] = row;
    else { at.set(row.message_id, rows.length); rows.push(row); }
  }
  return { recipients, rows, next_after: next.next_after ?? null };
}
