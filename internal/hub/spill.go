package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/XertroV/vecnet/internal/blob"
	"github.com/XertroV/vecnet/internal/envelope"
)

// Byte caps of a SendRequest (PROTOCOL §2.2 step 9). The 256 KiB whole-body
// cap is enforced by the HTTP layer.
const (
	MaxSendTextBytes  = 65536 // content.text of a SendRequest
	MaxMetadataBytes  = 8192  // metadata, serialised
	MaxJobInputBytes  = 16384 // job_request.input, serialised
	MaxJobOutputBytes = 8192  // job_status.output, serialised
)

// Schema limits a spill must stay inside: the spill adds one attachment and
// one metadata key to the Message.
const (
	maxAttachments  = 16
	maxMetadataKeys = 32
)

// SpillUploader is the blobs.uploader (and quota subject) of auto-spilled
// text: the hub creates those attachments, not the sender (PROTOCOL §2.2 step 6).
const SpillUploader = "hub"

// spillMediaType and spillName describe the hub-created spill attachment.
const (
	spillMediaType = "text/markdown"
	spillName      = "message.md"
)

// previewSeparatorWindow is how far back from the cap spillPreview looks
// for a newline or space to cut at.
const previewSeparatorWindow = 256

// spillPreview returns the longest prefix of text that is at most cap bytes
// and ends on a rune boundary, preferring to cut just before the last "\n"
// (else the last " ") within the final 256 bytes of that prefix. The result is always
// valid UTF-8 (invalid input bytes are first replaced with U+FFFD). No
// ellipsis is appended; Content.truncated carries that meaning.
func spillPreview(text string, cap int) (preview string) {
	if !utf8.ValidString(text) {
		text = strings.ToValidUTF8(text, "�")
	}
	if cap <= 0 {
		return ""
	}
	if len(text) <= cap {
		return text
	}
	end := cap
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	lo := max(0, end-previewSeparatorWindow)
	for _, sep := range []string{"\n", " "} {
		if i := strings.LastIndex(text[lo:end], sep); i >= 0 && lo+i > 0 {
			return text[:lo+i]
		}
	}
	return text[:end]
}

// textCap is Cfg.TextCap, or 4096 when unset.
func (h *Hub) textCap() int {
	if h.Cfg.TextCap > 0 {
		return h.Cfg.TextCap
	}
	return 4096
}

// needSpillError reports that a send must spill text whose bytes are not in
// the blob store yet. blob.Store.Put runs its own write transaction, so
// Send rolls back, stores the text with putSpill and runs the send again;
// only a send that passed every other check is charged for the upload.
type needSpillError struct{ text string }

func (e *needSpillError) Error() string { return "hub: spill blob not stored yet" }

// applySpill auto-spills msg when fullText exceeds Cfg.TextCap bytes
// (PROTOCOL §2.2 step 6): the full text, already stored by putSpill as a
// text/markdown blob of uploader "hub", is appended as the attachment
// {sha256, size, "text/markdown", "message.md"}; metadata["vecnet.spilled"]
// is set; content.text becomes the spillPreview and truncated true. A
// short text changes nothing; callers read content.truncated to learn
// whether msg spilled. A *needSpillError means the blob is not stored yet. A Message that has no
// room left for the extra attachment or metadata key is 413
// payload_too_large.
func (h *Hub) applySpill(ctx context.Context, tx *sql.Tx, msg *envelope.Message, fullText string) error {
	limit := h.textCap()
	if len(fullText) <= limit {
		return nil
	}
	if len(msg.Attachments) >= maxAttachments {
		e := E(http.StatusRequestEntityTooLarge, "payload_too_large",
			"content.text needs auto-spill but the message already has the maximum number of attachments")
		e.Details["field"] = "attachments"
		e.Details["limit"] = maxAttachments - 1
		return e
	}
	if len(msg.Metadata) >= maxMetadataKeys {
		e := E(http.StatusRequestEntityTooLarge, "payload_too_large",
			"content.text needs auto-spill but metadata has no room for vecnet.spilled")
		e.Details["field"] = "metadata"
		e.Details["limit"] = maxMetadataKeys - 1
		return e
	}
	sum := sha256.Sum256([]byte(fullText))
	sha := hex.EncodeToString(sum[:])
	if h.Blobs.Transfers != nil {
		if _, err := h.Blobs.PutMessageData(ctx, tx, SpillUploader, "spill", spillMediaType, []byte(fullText)); err != nil {
			return h.spillError(err)
		}
	}
	ok, err := h.Blobs.Exists(ctx, tx, sha)
	if err != nil {
		return err
	}
	if !ok && h.Blobs.Transfers == nil {
		return &needSpillError{text: fullText}
	}
	msg.Attachments = append(msg.Attachments, envelope.Attachment{
		SHA256: sha, Size: int64(len(fullText)), MediaType: spillMediaType, Name: spillName,
	})
	md := make(map[string]any, len(msg.Metadata)+1)
	for k, v := range msg.Metadata {
		md[k] = v
	}
	md["vecnet.spilled"] = true
	msg.Metadata = md
	msg.Content = envelope.Content{Text: spillPreview(fullText, limit), Truncated: true}
	return nil
}

// putSpill stores text as the hub-created spill blob, outside any
// transaction. Quota and purge failures map as for PUT /v1/blobs.
func (h *Hub) putSpill(ctx context.Context, text string) error {
	_, err := h.Blobs.Put(ctx, SpillUploader, strings.NewReader(text), spillMediaType, 0)
	return h.spillError(err)
}

func (h *Hub) spillError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, blob.ErrQuota):
		e := E(http.StatusTooManyRequests, "upload_quota_exceeded", "the hub's daily quota for spilled text is exhausted")
		e.Details["field"] = "content.text"
		e.Details["retry_after_seconds"] = secondsToUTCMidnight(h.Now())
		return e
	case errors.Is(err, blob.ErrPurged):
		e := E(http.StatusGone, "blob_purged", "this text was spilled before and purged by the operator")
		e.Details["field"] = "content.text"
		return e
	}
	return fmt.Errorf("hub: store spilled text: %w", err)
}

// checkAttachments checks the sender's attachments against the blob store
// (PROTOCOL §2.2 step 7, §11) and returns the set of hosted hashes. An
// attachment is hosted when its bytes are in the store (not purged); a
// hosted attachment's declared size must equal the stored size (422
// attachment_size_mismatch). An attachment whose bytes are not stored is
// reference-only when it names an origin_url and 422 unknown_blob
// otherwise. Hosted bytes that also name an origin_url stay hosted.
//
// A stored hash is not a read capability (PROTOCOL §11, §16.3): the sender
// may attach stored bytes only when it or its principal uploaded them, or
// a message referencing them is visible to it. Otherwise the hash is
// treated as not stored, and no error reveals the stored size.
func (h *Hub) checkAttachments(ctx context.Context, tx *sql.Tx, snd sender, atts []envelope.Attachment) (hosted map[string]bool, err error) {
	hosted = map[string]bool{}
	for i, a := range atts {
		if a.BlobID != "" {
			if h.Blobs.Transfers == nil {
				return nil, E(422, "unknown_blob", "unknown attachment")
			}
			if err := h.checkR2Attachment(ctx, tx, snd, &atts[i]); err != nil {
				return nil, err
			}
			continue
		}

		var size int64
		qerr := tx.QueryRowContext(ctx,
			`SELECT b.size FROM blobs b WHERE b.sha256 = ?1 AND b.purged_at IS NULL
			   AND (EXISTS (SELECT 1 FROM blob_uploaders u WHERE u.sha256 = b.sha256 AND u.uploader IN (?2, ?3))
			        OR EXISTS (SELECT 1 FROM blob_refs br
			                   JOIN messages m ON m.msg_id = br.msg_id
			                   JOIN threads t ON t.thr_id = m.thr_id
			                   JOIN thread_participants tp ON tp.thr_id = m.thr_id
			                   WHERE br.sha256 = b.sha256 AND tp.peer_id = ?2 AND `+visibleSQL+`))`,
			a.SHA256, snd.peer, snd.prn).Scan(&size)

		if errors.Is(qerr, sql.ErrNoRows) {
			qerr = tx.QueryRowContext(ctx, `SELECT length(p.bytes) FROM durable_blob_payloads p
                WHERE p.sha256=?1 AND NOT EXISTS(SELECT 1 FROM blob_purge_tombstones WHERE sha256=p.sha256)
                AND EXISTS(SELECT 1 FROM durable_blob_refs br JOIN messages m ON m.msg_id=br.msg_id
                JOIN threads t ON t.thr_id=m.thr_id JOIN thread_participants tp ON tp.thr_id=m.thr_id
                WHERE br.sha256=p.sha256 AND br.purpose=p.purpose AND tp.peer_id=?2 AND `+visibleSQL+`) LIMIT 1`, a.SHA256, snd.peer).Scan(&size)
		}
		switch {
		case errors.Is(qerr, sql.ErrNoRows):
			// PROTOCOL section 11: a reference-only origin remains external even
			// when its hash coincides with an R2 object. Never probe or fetch it.
			if a.OriginURL != "" {
				continue
			}
			if h.Cfg.BlobBackend == "r2" {
				// Uniform for known and unknown hashes: presence is not a capability.
				return nil, E(422, "blob_id_required", "blob_id is required for remote attachments")
			}
			e := E(http.StatusUnprocessableEntity, "unknown_blob", "attachment hash not uploaded and no origin_url")
			e.Details["path"] = fmt.Sprintf("/attachments/%d/sha256", i)
			e.Details["sha256"] = a.SHA256
			return nil, e
		case qerr != nil:
			return nil, fmt.Errorf("hub: attachment lookup: %w", qerr)
		}
		if size != a.Size {
			e := E(http.StatusUnprocessableEntity, "attachment_size_mismatch", "attachment size differs from the stored blob")
			e.Details["path"] = fmt.Sprintf("/attachments/%d/size", i)
			e.Details["sha256"] = a.SHA256
			return nil, e
		}
		hosted[a.SHA256] = true
	}
	return hosted, nil
}

// deliveryBlobURLs returns a copy of msg for recipient in which every
// hosted attachment carries a blob_url whose token is scoped to (recipient,
// sha256) and expires at now + Cfg.BlobTokenTTL (PROTOCOL §2.7, §11).
// Reference-only attachments get no blob_url. msg itself is not changed:
// the stored Message never holds a blob_url.
func (h *Hub) deliveryBlobURLs(msg envelope.Message, recipient string, hosted map[string]bool) envelope.Message {
	if len(msg.Attachments) == 0 {
		return msg
	}
	exp := h.Now().Add(h.Cfg.BlobTokenTTL)
	atts := make([]envelope.Attachment, len(msg.Attachments))
	for i, a := range msg.Attachments {
		a.BlobURL = ""
		if a.BlobID == "" && hosted[a.SHA256] {
			a.BlobURL = h.Blobs.URL(h.Cfg.PublicURL, recipient, a.SHA256, exp)
		}
		atts[i] = a
	}
	msg.Attachments = atts
	return msg
}

// checkPayloadCaps enforces the byte caps of PROTOCOL §2.2 step 9 on the raw
// body with 413 payload_too_large and details.field. It runs before schema
// validation, whose maxLength counts characters, not bytes. A body it
// cannot read is left to the schema.
func checkPayloadCaps(raw []byte) error {
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil {
		return nil
	}
	tooLarge := func(field string, limit int) error {
		e := E(http.StatusRequestEntityTooLarge, "payload_too_large", field+" exceeds its byte cap")
		e.Details["field"] = field
		e.Details["limit"] = limit
		return e
	}
	var content struct {
		Text json.RawMessage `json:"text"`
	}
	if json.Unmarshal(top["content"], &content) == nil {
		var text string
		if json.Unmarshal(content.Text, &text) == nil && len(text) > MaxSendTextBytes {
			return tooLarge("content.text", MaxSendTextBytes)
		}
	}
	if serialisedLen(top["metadata"]) > MaxMetadataBytes {
		return tooLarge("metadata", MaxMetadataBytes)
	}
	sub := func(obj, key string) json.RawMessage {
		var m map[string]json.RawMessage
		if json.Unmarshal(top[obj], &m) != nil {
			return nil
		}
		return m[key]
	}
	if serialisedLen(sub("job_request", "input")) > MaxJobInputBytes {
		return tooLarge("job_request.input", MaxJobInputBytes)
	}
	if serialisedLen(sub("job_status", "output")) > MaxJobOutputBytes {
		return tooLarge("job_status.output", MaxJobOutputBytes)
	}
	return nil
}

// serialisedLen is the length of v re-encoded without insignificant
// whitespace; 0 when v is absent or not JSON.
func serialisedLen(v json.RawMessage) int {
	if len(v) == 0 {
		return 0
	}
	var buf bytes.Buffer
	if json.Compact(&buf, v) != nil {
		return 0
	}
	return buf.Len()
}
