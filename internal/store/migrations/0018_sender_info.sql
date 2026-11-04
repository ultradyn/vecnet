-- Message.sender_info (PROTOCOL §2.2 step 12) for messages stored before it
-- existed. New messages carry the sender's directory name and kind as of the
-- send, written into the body by the hub. This backfill can only use the
-- directory as of the migration, so for these rows it is not a send-time
-- snapshot (PROTOCOL §8.2). It fills the stored body, then copies that value
-- into every delivery Event of the same message, so the event log, push
-- bodies and thread history agree. Rows that already have sender_info, and
-- rows whose sender has no directory entry, are left as they are. Pending
-- event_outbox bodies keep their exact signed bytes.
UPDATE messages SET body = json_set(messages.body, '$.sender_info', json_object('name', p.name, 'kind', p.kind))
FROM peers p
WHERE p.peer_id = messages.sender AND json_type(messages.body, '$.sender_info') IS NULL;

UPDATE messages SET body = json_set(messages.body, '$.sender_info', json_object('name', s.name, 'kind', 'source'))
FROM sources s
WHERE s.src_id = messages.sender AND json_type(messages.body, '$.sender_info') IS NULL;

UPDATE messages SET body = json_set(body, '$.sender_info', json_object('name', 'hub', 'kind', 'hub'))
WHERE sender = 'hub' AND json_type(body, '$.sender_info') IS NULL;

UPDATE events SET data = json_set(events.data, '$.message.sender_info', json(json_extract(m.body, '$.sender_info')))
FROM messages m
WHERE events.type = 'delivery'
  AND m.msg_id = json_extract(events.data, '$.message.message_id')
  AND json_type(events.data, '$.message.sender_info') IS NULL
  AND json_type(m.body, '$.sender_info') = 'object';
