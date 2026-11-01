package oauth

import "time"

// Limits the external tests drive.
const (
	MaxClients    = maxClients
	ClientIdleAge = clientIdleAge
)

// CIMDMaxFetches is the gate's limit.
const CIMDMaxFetches = cimdMaxFetches

// CIMDGate exposes the gate's two calls.
type CIMDGate struct{ g cimdGate }

func (c *CIMDGate) Enter(id string, now time.Time) bool { return c.g.enter(id, now) }

func (c *CIMDGate) Leave(id string, ok bool, now time.Time) { c.g.leave(id, ok, now) }
