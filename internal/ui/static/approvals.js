// Pure approval logic shared by the inbox and thread screens.

const two = n => String(n).padStart(2, '0');

// hubNowMs is store.hubNow(): the client's own clock may be minutes out. Seconds
// round up, so the text reaches 00:00 exactly when the approval expires.
export function countdown(expiresAtIso, hubNowMs) {
  const left = Date.parse(expiresAtIso) - hubNowMs;
  if (!(left > 0)) return { text: '00:00', urgent: true, expired: true };
  const total = Math.ceil(left / 1000);
  const h = Math.floor(total / 3600), m = Math.floor(total / 60) % 60, s = total % 60;
  return { text: (h ? h + ':' : '') + two(m) + ':' + two(s), urgent: left < 60000, expired: false };
}

// The key is fixed per approval and option, so a double click or a retry cannot
// answer twice (Review Focus 2).
export function answerBody(approval, optionIndex) {
  const options = approval.options || [];
  if (!Number.isInteger(optionIndex) || optionIndex < 0 || optionIndex >= options.length) {
    throw new RangeError('approval has no option ' + optionIndex);
  }
  const decision = options[optionIndex];
  return {
    idempotency_key: 'ui-apr-' + approval.approval_id + '-' + optionIndex,
    thread_id: approval.thread_id,
    in_reply_to: approval.message_id,
    kind: 'approval_response',
    priority: 'wake',
    content: { text: decision },
    approval_response: { approval_id: approval.approval_id, decision },
  };
}
