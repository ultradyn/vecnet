-- Isolated R2 objects and durable message content. Additive: disk tables stay intact.
-- No credentials, capabilities or preference store are persisted here.
CREATE TABLE blob_objects (
 blob_id TEXT PRIMARY KEY NOT NULL CHECK(length(blob_id)=30 AND substr(blob_id,1,4)='blo_' AND substr(blob_id,5,1) GLOB '[0-7]' AND substr(blob_id,5) NOT GLOB '*[^0-9A-HJKMNP-TV-Z]*'),
 sha256 TEXT NOT NULL CHECK(length(sha256)=64 AND sha256 NOT GLOB '*[^0-9a-f]*'),
 backend TEXT NOT NULL DEFAULT 'r2' CHECK(backend='r2'),
 object_key TEXT NOT NULL UNIQUE,
 uploader TEXT NOT NULL,
 size INTEGER NOT NULL CHECK(size BETWEEN 1 AND 26214400),
 media_type TEXT NOT NULL CHECK(length(media_type) BETWEEN 1 AND 128),
 issued_at TEXT NOT NULL CHECK(julianday(issued_at) IS NOT NULL),
 upload_deadline TEXT NOT NULL CHECK(julianday(upload_deadline) IS NOT NULL AND abs((julianday(upload_deadline)-julianday(issued_at))*86400-300)<0.001),
 retention_class TEXT NOT NULL CHECK(retention_class IN ('1h','6h','12h','1d','7d','30d','keep')),
 expiry_pref_value TEXT NOT NULL,
 expiry_pref_source TEXT NOT NULL CHECK(expiry_pref_source IN ('default','flag','stored')),
 expiry_pref_revision INTEGER NOT NULL CHECK(expiry_pref_revision>=0),
 expires_at TEXT,
 first_attach_deadline TEXT CHECK(first_attach_deadline IS NULL OR (retention_class='keep' AND julianday(first_attach_deadline) IS NOT NULL)),
 checksum_profile TEXT NOT NULL CHECK(checksum_profile='sha256'),
 expected_checksum TEXT NOT NULL CHECK(expected_checksum=sha256),
 state TEXT NOT NULL CHECK(state IN ('pending','complete','rejected','abandoned','expired','purged')),
 sha256_verified INTEGER NOT NULL DEFAULT 0 CHECK(sha256_verified IN (0,1)),
 verified_algorithm TEXT,
 verified_digest TEXT,
 verified_size INTEGER,
 etag TEXT,
 verified_at TEXT,
 purged_at TEXT CHECK(purged_at IS NULL OR julianday(purged_at) IS NOT NULL),
 deletion_confirmed_at TEXT CHECK(deletion_confirmed_at IS NULL OR julianday(deletion_confirmed_at) IS NOT NULL),
 storage_charge_state TEXT NOT NULL DEFAULT 'reserved' CHECK(storage_charge_state IN ('reserved','retained','released')),
 CHECK((retention_class='keep' AND expiry_pref_value='off' AND expires_at IS NULL)
  OR (retention_class<>'keep' AND expiry_pref_value=retention_class AND expires_at IS NOT NULL AND julianday(expires_at) IS NOT NULL
   AND abs((julianday(expires_at)-julianday(issued_at))*86400 - CASE retention_class WHEN '1h' THEN 3600 WHEN '6h' THEN 21600 WHEN '12h' THEN 43200 WHEN '1d' THEN 86400 WHEN '7d' THEN 604800 WHEN '30d' THEN 2592000 END)<0.001)),
 CHECK((retention_class='keep' AND object_key GLOB ('attachments/v1/keep/[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]/' || blob_id))
  OR (retention_class<>'keep' AND object_key GLOB ('attachments/v1/exp/' || retention_class || '/[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]/' || blob_id))),
 CHECK((sha256_verified=0 AND verified_algorithm IS NULL AND verified_digest IS NULL AND verified_size IS NULL AND etag IS NULL AND verified_at IS NULL)
  OR (sha256_verified=1 AND verified_algorithm IS NOT NULL AND verified_algorithm='sha256' AND verified_digest IS NOT NULL AND verified_digest=sha256 AND verified_size IS NOT NULL AND verified_size=size AND etag IS NOT NULL AND length(etag)>0 AND verified_at IS NOT NULL AND julianday(verified_at) IS NOT NULL)),
 CHECK(state<>'pending' OR sha256_verified=0),
 CHECK(state<>'complete' OR sha256_verified=1)
) STRICT;
CREATE INDEX blob_objects_uploader_state ON blob_objects(uploader, state);
CREATE INDEX blob_objects_state_expiry ON blob_objects(state, expires_at);
CREATE INDEX blob_objects_sha256 ON blob_objects(sha256);

CREATE TABLE blob_upload_requests (
 uploader TEXT NOT NULL,
 idempotency_key TEXT NOT NULL CHECK(length(idempotency_key)>0),
 request_digest TEXT NOT NULL CHECK(length(request_digest)=64 AND request_digest NOT GLOB '*[^0-9a-f]*'),
 blob_id TEXT NOT NULL REFERENCES blob_objects(blob_id),
 issue_day TEXT NOT NULL CHECK(length(issue_day)=10 AND date(issue_day)=issue_day),
 PRIMARY KEY(uploader,idempotency_key)
) STRICT;
CREATE INDEX blob_upload_requests_issue_day ON blob_upload_requests(issue_day);

CREATE TABLE blob_object_refs (
 msg_id TEXT NOT NULL REFERENCES messages(msg_id) ON DELETE CASCADE,
 blob_id TEXT NOT NULL REFERENCES blob_objects(blob_id),
 sha256 TEXT NOT NULL CHECK(length(sha256)=64 AND sha256 NOT GLOB '*[^0-9a-f]*'),
 PRIMARY KEY(msg_id,blob_id)
) STRICT;
CREATE INDEX blob_object_refs_object ON blob_object_refs(blob_id);
CREATE INDEX blob_object_refs_sha256 ON blob_object_refs(sha256);

CREATE TABLE blob_r2_quota (
 uploader TEXT NOT NULL,
 day TEXT NOT NULL CHECK(length(day)=10 AND date(day)=day),
 reserved_bytes INTEGER NOT NULL DEFAULT 0 CHECK(reserved_bytes>=0),
 accepted_bytes INTEGER NOT NULL DEFAULT 0 CHECK(accepted_bytes>=0),
 abandoned_bytes INTEGER NOT NULL DEFAULT 0 CHECK(abandoned_bytes>=0),
 rejected_bytes INTEGER NOT NULL DEFAULT 0 CHECK(rejected_bytes>=0),
 PRIMARY KEY(uploader,day)
) STRICT;
CREATE INDEX blob_r2_quota_day ON blob_r2_quota(day);

CREATE TABLE blob_r2_storage (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 storage_reserved_bytes INTEGER NOT NULL DEFAULT 0 CHECK(storage_reserved_bytes>=0),
 retained_bytes INTEGER NOT NULL DEFAULT 0 CHECK(retained_bytes>=0)
) STRICT;
INSERT INTO blob_r2_storage(singleton) VALUES(1);

CREATE TABLE durable_blob_payloads (
 sha256 TEXT NOT NULL CHECK(length(sha256)=64 AND sha256 NOT GLOB '*[^0-9a-f]*'),
 purpose TEXT NOT NULL CHECK(purpose IN ('spill','source_raw')),
 bytes BLOB NOT NULL,
 media_type TEXT NOT NULL CHECK(length(media_type) BETWEEN 1 AND 128),
 created_at TEXT NOT NULL CHECK(julianday(created_at) IS NOT NULL),
 PRIMARY KEY(sha256,purpose),
 CHECK(purpose<>'spill' OR length(bytes)<=65536)
) STRICT;

CREATE TABLE durable_blob_refs (
 msg_id TEXT NOT NULL REFERENCES messages(msg_id) ON DELETE CASCADE,
 sha256 TEXT NOT NULL,
 purpose TEXT NOT NULL,
 PRIMARY KEY(msg_id,sha256,purpose),
 FOREIGN KEY(sha256,purpose) REFERENCES durable_blob_payloads(sha256,purpose)
) STRICT;
CREATE INDEX durable_blob_refs_payload ON durable_blob_refs(sha256,purpose);

CREATE TABLE blob_purge_tombstones (
 sha256 TEXT PRIMARY KEY NOT NULL CHECK(length(sha256)=64 AND sha256 NOT GLOB '*[^0-9a-f]*'),
 purged_at TEXT NOT NULL CHECK(julianday(purged_at) IS NOT NULL),
 actor TEXT NOT NULL
) STRICT;

CREATE TABLE blob_delete_jobs (
 object_key TEXT PRIMARY KEY NOT NULL,
 blob_id TEXT NOT NULL REFERENCES blob_objects(blob_id),
 not_before TEXT NOT NULL CHECK(julianday(not_before) IS NOT NULL),
 attempts INTEGER NOT NULL DEFAULT 0 CHECK(attempts>=0),
 next_attempt TEXT NOT NULL CHECK(julianday(next_attempt) IS NOT NULL),
 last_code TEXT CHECK(last_code IS NULL OR (length(last_code) BETWEEN 1 AND 64 AND last_code NOT GLOB '*[^A-Za-z0-9_]*'))
) STRICT;
CREATE INDEX blob_delete_jobs_next ON blob_delete_jobs(next_attempt);

CREATE TABLE blob_usage_events (
 event_id TEXT PRIMARY KEY NOT NULL,
 blob_id TEXT NOT NULL,
 uploader TEXT NOT NULL,
 day TEXT NOT NULL CHECK(length(day)=10 AND date(day)=day),
 caller_identity TEXT,
 kind TEXT NOT NULL CHECK(kind IN ('reserve','verified','rejected','abandoned','expired','purged','last_ref_removed','storage_released','delete_confirmed','download_grant_issued','download_grant_rate_limited')),
 byte_count INTEGER NOT NULL CHECK(byte_count>=0),
 at TEXT NOT NULL CHECK(julianday(at) IS NOT NULL),
 CHECK(kind NOT IN ('download_grant_issued','download_grant_rate_limited') OR (caller_identity IS NOT NULL AND length(caller_identity)>0 AND byte_count=0))
) STRICT;
-- Capability mints are intentionally NOT unique by blob: each issuance is metered.
CREATE UNIQUE INDEX blob_usage_events_transition ON blob_usage_events(blob_id,kind) WHERE kind NOT IN ('download_grant_issued','download_grant_rate_limited');
CREATE INDEX blob_usage_events_day ON blob_usage_events(day);
CREATE INDEX blob_usage_events_uploader_day ON blob_usage_events(uploader,day);
