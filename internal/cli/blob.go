package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/XertroV/vecnet/internal/envelope"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/XertroV/vecnet/internal/blobclient"
	"github.com/XertroV/vecnet/internal/blobwire"
)

type blobHubTransport struct{ c *client }

func (t blobHubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	if t.c.secret != "" {
		r.Header.Set("Authorization", "Bearer "+t.c.secret)
	}
	if t.c.clientHeader != "" {
		r.Header.Set("X-Vecnet-Client", t.c.clientHeader)
	}
	rt := t.c.hc.Transport
	if rt == nil {
		rt = http.DefaultTransport
	}
	return rt.RoundTrip(r)
}
func (c *client) blobs() *blobclient.Client {
	return blobclient.New(c.base, blobHubTransport{c}, nil, nil)
}
func blobError(err error) error {
	var e *blobclient.Error
	if errors.As(err, &e) {
		return &hubError{Status: e.Status, Code: e.Code, Message: e.Error(), Details: map[string]any{"retry_after_seconds": e.RetryAfterSeconds}}
	}
	return err
}
func (a *app) cmdBlob(args []string) error {
	if len(args) == 0 || args[0] != "get" {
		return usageErr("blob get SHA [--blob-id ID] --out PATH [--json]")
	}
	o, err := parseFlags("blob get", args[1:], map[string]flagKind{"blob-id": strFlag, "out": strFlag})
	if err != nil {
		return err
	}
	if len(o.pos) != 1 || o.str("out") == "" {
		return usageErr("blob get SHA [--blob-id ID] --out PATH [--json]")
	}
	path := o.str("out")
	if _, err := os.Lstat(path); err == nil {
		return usageErr("blob get: output already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	c, _, _, err := a.connect()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(a.ctx, uploadTimeout)
	defer cancel()
	// Same directory keeps publication atomic. CreateTemp uses 0600, including
	// while unverified bytes are being streamed to this private file.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".vecnet-blob-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	g, err := c.blobs().DownloadTo(ctx, tmp, o.pos[0], o.str("blob-id"), blobclient.MaxSize)
	if err != nil {
		if a.json {
			var e *blobclient.Error
			if errors.As(err, &e) {
				_ = a.emitValue(map[string]any{"error": map[string]any{"code": e.Code, "retry_after_seconds": e.RetryAfterSeconds}})
			}
		}
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	// Link publishes the verified inode atomically with no-replace semantics,
	// including if a destination appeared while the download was in progress.
	if err = os.Link(tmp.Name(), path); err != nil {
		return usageErr("blob get: cannot publish output (destination may already exist)")
	}
	ref := blobwire.Ref{BlobID: g.BlobID, SHA256: g.SHA256, Size: g.Size, ExpiresAt: g.ObjectExpiresAt, RetentionClass: g.RetentionClass, SHA256Verified: g.SHA256Verified, Integrity: g.Integrity}
	if a.json {
		return a.emitValue(map[string]any{"reference": ref, "status": "saved"})
	}
	fmt.Fprintf(a.stdout, "saved %s (%d bytes)\n", path, g.Size)
	return nil
}

func blobLabels(atts []envelope.Attachment, states map[string]blobwire.State) []string {
	var labels []string
	for _, att := range atts {
		if att.BlobID == "" {
			continue
		}
		name := unbidi(flatten(att.Name))
		state := states[att.BlobID].State
		if state != "purged" && att.ExpiresAt != nil {
			if at, err := time.Parse(time.RFC3339Nano, *att.ExpiresAt); err == nil && !time.Now().Before(at) {
				state = "expired"
			}
		}
		label := "[attachment: " + name + "]"
		if state == "expired" || state == "purged" {
			label = "[attachment " + state + ": " + name + "]"
		}
		retention := map[string]string{"1h": "1-hour retention", "6h": "6-hour retention", "12h": "12-hour retention", "1d": "24-hour retention", "7d": "7-day retention", "30d": "30-day retention", "keep": "Kept while referenced"}[att.RetentionClass]
		if retention != "" {
			label += " (" + retention + ")"
		}
		labels = append(labels, label)
	}
	return labels
}
func eventBlobLabels(ev envelope.Event) string {
	if ev.Type != "delivery" {
		return ""
	}
	var d envelope.Delivery
	if json.Unmarshal(ev.Data, &d) != nil {
		return ""
	}
	return strings.Join(blobLabels(d.Message.Attachments, ev.BlobStates), " ")
}
