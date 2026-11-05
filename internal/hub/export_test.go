package hub

import (
	"time"

	"github.com/XertroV/vecnet/internal/store"
)

// SpillPreview exposes spillPreview to the external hub_test package.
var SpillPreview = spillPreview

// WaitNotifies blocks until every notification webhook started so far has
// finished (delivered, dropped or out of retries).
func (h *Hub) WaitNotifies() { h.notifyWG.Wait() }

// NewPreferences exposes newPreferences, including installed gated adapters,
// to the external hub_test package.
func NewPreferences(db *store.DB, cfg Config, now func() time.Time, installed ...string) (*Preferences, error) {
	return newPreferences(db, cfg, now, installed...)
}

// SubscriberCount reports the live preference subscriptions.
func (p *Preferences) SubscriberCount() int {
	p.subMu.Lock()
	defer p.subMu.Unlock()
	return len(p.subscribers)
}
