# 36: Delivery carries no sender name/kind snapshot

Requested 2026-11-04 by cc-pagchat-cos (pag0 log review, thr_01M6JQ9FXB4ZNAET860AZ4BSXP). pagd calls `GET /v1/peers` per inbound delivery just to learn the sender's directory name and kind: an extra round trip, and a directory failure stalls the delivery. Names can also change after the send.

Proposed: additive `sender_info {name, kind}` on Delivery (and the Message it wraps), snapshotted at send time and stored with the message so it is immutable per message; older rows fall back to the current directory entry. PROTOCOL Delivery shape + §2 text; CLI/MCP/UI may prefer it over a directory lookup. Use case: pag-chat gives operator-kind senders a higher trust tier than session peers (pairs with operator-cli's explicit operator profile, so the identity a send uses is obvious).
