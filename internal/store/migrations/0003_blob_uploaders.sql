-- Every peer or principal that has uploaded a blob's bytes (PROTOCOL §11).
-- blobs.uploader keeps only the first; attaching an existing hash requires
-- the sender (or its principal) to be listed here, or to see a message
-- referencing the blob, so a hash alone is never a read capability.
CREATE TABLE blob_uploaders (
	sha256   TEXT NOT NULL,
	uploader TEXT NOT NULL,
	PRIMARY KEY (sha256, uploader)
) STRICT;

INSERT OR IGNORE INTO blob_uploaders(sha256, uploader) SELECT sha256, uploader FROM blobs;
