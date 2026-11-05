package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/XertroV/vecnet/internal/blob"
	"github.com/XertroV/vecnet/internal/blobwire"
	"github.com/XertroV/vecnet/internal/envelope"
)

// validateBlobSend retains the disk schema and validates direct references
// against the existing strict attachmentReference contract. Only validated R2
// fields are removed from the copy used for legacy request validation; the
// original bytes remain the canonical request and stored Message input.
func (h *Hub) validateBlobSend(raw []byte) error {
	if h.Blobs.Transfers == nil {
		return envelope.Validate("sendRequest", raw)
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil {
		return envelope.Validate("sendRequest", raw)
	}
	var atts []json.RawMessage
	if json.Unmarshal(top["attachments"], &atts) != nil {
		return envelope.Validate("sendRequest", raw)
	}
	changed := false
	for i, rawAtt := range atts {
		var a map[string]json.RawMessage
		if json.Unmarshal(rawAtt, &a) != nil {
			continue
		}
		if _, ok := a["blob_id"]; !ok {
			continue
		}
		if err := envelope.Validate("attachmentReference", rawAtt); err != nil {
			var e *envelope.SchemaError
			if errors.As(err, &e) {
				return &envelope.SchemaError{Path: fmt.Sprintf("/attachments/%d", i) + e.Path, Message: e.Message}
			}
			return err
		}
		delete(a, "blob_id")
		delete(a, "retention_class")
		delete(a, "expires_at")
		atts[i], _ = json.Marshal(a)
		changed = true
	}
	if !changed {
		return envelope.Validate("sendRequest", raw)
	}
	top["attachments"], _ = json.Marshal(atts)
	copyRaw, _ := json.Marshal(top)
	return envelope.Validate("sendRequest", copyRaw)
}
func (h *Hub) checkR2Attachment(ctx context.Context, tx *sql.Tx, snd sender, a *envelope.Attachment) error {
	o, err := blob.ReadR2Object(ctx, tx, a.BlobID)
	allowed := err == nil && (o.Uploader == snd.peer || snd.prn != "" && o.Uploader == snd.prn)
	if err == nil && !allowed {
		allowed, err = h.r2BlobVisible(ctx, tx, snd.peer, "", a.SHA256, a.BlobID)
	}
	if err != nil || !allowed || o.SHA256 != a.SHA256 {
		return E(422, "unknown_blob", "unknown attachment")
	}
	if !o.SHA256Verified {
		return h.blobTransferError(blob.ErrR2Incomplete)
	}
	var purged int
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM blob_purge_tombstones WHERE sha256=?1) OR EXISTS(SELECT 1 FROM blobs WHERE sha256=?1 AND purged_at IS NOT NULL)`, o.SHA256).Scan(&purged); err != nil {
		return h.blobTransferError(err)
	}
	if purged != 0 || o.State == "purged" {
		return h.blobTransferError(blob.ErrR2Purged)
	}
	if o.State == "expired" || o.ExpiresAt != nil && !h.Now().Before(*o.ExpiresAt) {
		return h.blobTransferError(blob.ErrR2Expired)
	}
	if o.State != "complete" || !o.SHA256Verified {
		return h.blobTransferError(blob.ErrR2Incomplete)
	}
	if a.Size != o.Size {
		return E(422, "attachment_size_mismatch", "attachment size differs from stored blob")
	}
	expiry := ""
	if o.ExpiresAt != nil {
		expiry = envelope.FormatTime(*o.ExpiresAt)
	}
	declared := ""
	if a.ExpiresAt != nil {
		declared = *a.ExpiresAt
	}
	if a.RetentionClass != o.RetentionClass || declared != expiry || (a.ExpiresAt == nil) != (o.ExpiresAt == nil) {
		return E(422, "invalid_value", "attachment retention differs from stored blob")
	}
	verified := o.SHA256Verified
	a.SHA256Verified = &verified
	a.Integrity = &blobwire.Integrity{Algorithm: "sha256", Value: o.SHA256, Verified: true}
	return nil
}
