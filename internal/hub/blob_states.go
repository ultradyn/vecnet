package hub

import (
	"context"
	"encoding/json"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/blob"
	"github.com/XertroV/vecnet/internal/blobwire"
	"github.com/XertroV/vecnet/internal/envelope"
)

// ProjectBlobStates decorates a read without changing Message or event bytes.
// The supplied messages select references, not authority: each exact reference
// must belong to a retained message visible under current from_join rules.
// This is a metadata projection, not an S3 availability probe or URL mint.
func (h *Hub) ProjectBlobStates(ctx context.Context, caller auth.Caller, messages []envelope.Message) (map[string]blobwire.State, error) {
	out := map[string]blobwire.State{}
	hasRemote := false
	for _, m := range messages {
		for _, a := range m.Attachments {
			if a.BlobID != "" {
				hasRemote = true
				break
			}
		}
		if hasRemote {
			break
		}
	}
	if !hasRemote {
		return out, nil
	}
	if !h.callerActive(ctx, h.DB, caller) {
		return out, ctx.Err()
	}
	now := h.Now()
	for _, m := range messages {
		for _, a := range m.Attachments {
			if a.BlobID == "" {
				continue
			}
			if _, ok := out[a.BlobID]; ok {
				continue
			}
			var visible bool
			err := h.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM blob_object_refs br
 JOIN messages m ON m.msg_id=br.msg_id JOIN threads t ON t.thr_id=m.thr_id
 WHERE br.msg_id=?1 AND br.blob_id=?2 AND br.sha256=?3 AND
 (?4='operator' OR EXISTS(SELECT 1 FROM thread_participants tp JOIN peers p ON p.peer_id=tp.peer_id
 WHERE tp.thr_id=m.thr_id AND p.disabled=0 AND ((?5<>'' AND p.peer_id=?5) OR (?6<>'' AND p.prn_id=?6)) AND `+visibleSQL+`)))`, m.MessageID, a.BlobID, a.SHA256, caller.Kind, caller.PeerID, principalReadID(caller)).Scan(&visible)
			if err != nil {
				return nil, h.blobTransferError(err)
			}
			if !visible {
				continue
			}
			state, err := blob.ReadR2State(ctx, h.DB, a.BlobID, now)
			if err != nil {
				return nil, h.blobTransferError(err)
			}
			out[a.BlobID] = state
		}
	}
	return out, nil
}

// ProjectEventBlobStates returns fresh presentation fields beside unchanged
// durable event data. MCP adapters can use this same seam in their later slice.
func (h *Hub) ProjectEventBlobStates(ctx context.Context, caller auth.Caller, events []envelope.Event) ([]envelope.Event, error) {
	out := append([]envelope.Event(nil), events...)
	for i := range out {
		out[i].BlobStates = nil
		if out[i].Type != "delivery" {
			continue
		}
		var d envelope.Delivery
		if err := json.Unmarshal(out[i].Data, &d); err != nil {
			return nil, h.blobTransferError(err)
		}
		states, err := h.ProjectBlobStates(ctx, caller, []envelope.Message{d.Message})
		if err != nil {
			return nil, err
		}
		out[i].BlobStates = states
	}
	return out, nil
}
