# 31: Inbox reaction items should show and link their target

From the reactions branch review (design-5, deferred 2026-11-04). An operator inbox Delivery of `kind: reaction` shows no excerpt of the message reacted to and no link to it: inbox delivery items carry no reaction target, and the web router has no message anchor.

## Shape
- opview inbox item gains `reaction: {target_message_id, emoji, excerpt}` for `kind: reaction` deliveries (excerpt flattened, capped like other inbox excerpts).
- Web router: `#thread/<id>/m/<message_id>` scrolls to and highlights the message (reuse `scrollMessageIntoView`).
- Inbox card: "👍 on: <excerpt>" with the link.

## Order
Small; fold into the next web UI batch.
