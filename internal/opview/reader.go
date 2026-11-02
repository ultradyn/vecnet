// Package opview provides the operator's read models over the hub.
package opview

import "github.com/XertroV/vecnet/internal/hub"

// Reader shares the hub's database and clock. Each read model is implemented
// in its own file so later operator tasks can fill the agreed seams separately.
type Reader struct{ Hub *hub.Hub }

func New(h *hub.Hub) *Reader { return &Reader{Hub: h} }
