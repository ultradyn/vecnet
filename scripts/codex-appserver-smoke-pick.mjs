// Candidates have already passed the new-thread and smoke-cwd checks. Their
// turns are oldest first, read with itemsView: 'full' by the read-only observer.
export function pickSmokeThread(candidates, marker) {
  if (typeof marker !== 'string' || !marker) throw new Error('missing smoke prompt marker');
  const marked = candidates.filter(candidate => {
    const first = (candidate.turns ?? []).flatMap(turn => turn.items ?? [])
      .find(item => item?.type === 'userMessage');
    const text = typeof first?.text === 'string' ? first.text
      : (first?.content ?? []).filter(part => part?.type === 'text' && typeof part.text === 'string')
        .map(part => part.text).join('');
    return text.includes(marker);
  });
  if (marked.length > 1) {
    throw Object.assign(new Error(`ambiguous: ${marked.length} new threads with the smoke cwd and prompt marker: ${marked.map(th => th.id).join(', ')}`), { fatal: true });
  }
  return marked[0]?.id;
}
