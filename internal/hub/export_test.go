package hub

// SpillPreview exposes spillPreview to the external hub_test package.
var SpillPreview = spillPreview

// WaitNotifies blocks until every notification webhook started so far has
// finished (delivered, dropped or out of retries).
func (h *Hub) WaitNotifies() { h.notifyWG.Wait() }
