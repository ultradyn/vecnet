package httpapi

import (
	"fmt"
	"sync"
	"time"
)

const DefaultRestartRetry = 5 * time.Second
const drainFlush = time.Second

// Drain announces a transport-only restart to the current generation of
// streams. The zero value is usable; a nil Drain never announces anything.
type Drain struct {
	mu         sync.Mutex
	generation *drainGeneration
}

type drainGeneration struct {
	signal    chan struct{}
	retry     time.Duration
	announced bool
	streams   int
	idle      chan struct{} // closed whenever all joined streams have finished
}

func newDrainGeneration() *drainGeneration {
	idle := make(chan struct{})
	close(idle)
	return &drainGeneration{signal: make(chan struct{}), retry: DefaultRestartRetry, idle: idle}
}

func NewDrain() *Drain { return &Drain{generation: newDrainGeneration()} }

// current is called with mu held.
func (d *Drain) current() *drainGeneration {
	if d.generation == nil {
		d.generation = newDrainGeneration()
	}
	return d.generation
}

// Announce closes the current generation once. The first retry hint wins.
func (d *Drain) Announce(retryAfter time.Duration) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	g := d.current()
	if !g.announced {
		g.retry = retryAfter
		g.announced = true
		close(g.signal)
	}
}

func (d *Drain) C() <-chan struct{} {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.current().signal
}

func (d *Drain) RetryAfter() time.Duration {
	if d == nil {
		return DefaultRestartRetry
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.current().retry
}

// drainHandle pins a response's announcement and retry hint to the same
// generation as its registration, including across a demo Reset.
type drainHandle struct {
	d *Drain
	g *drainGeneration
}

func (h drainHandle) C() <-chan struct{} {
	if h.g == nil {
		return nil
	}
	return h.g.signal
}

func (h drainHandle) RetryAfter() time.Duration {
	if h.d == nil {
		return DefaultRestartRetry
	}
	h.d.mu.Lock()
	defer h.d.mu.Unlock()
	return h.g.retry
}

// join atomically registers and captures the generation. Handlers retain the
// handle until after their final frame; they never re-read the current one.
func (d *Drain) join() (drainHandle, func()) {
	if d == nil {
		return drainHandle{}, func() {}
	}
	d.mu.Lock()
	g := d.current()
	if g.streams == 0 {
		g.idle = make(chan struct{})
	}
	g.streams++
	d.mu.Unlock()
	var once sync.Once
	return drainHandle{d: d, g: g}, func() {
		once.Do(func() {
			d.mu.Lock()
			defer d.mu.Unlock()
			g.streams--
			if g.streams == 0 {
				close(g.idle)
			}
		})
	}
}

// Join registers one response; done is idempotent and belongs to the joined
// generation even if a demo Reset happens before it finishes.
func (d *Drain) Join() (done func()) {
	_, done = d.join()
	return done
}

// Wait waits for the joined generation without leaving a waiting goroutine
// behind on timeout. Concurrent joins and completions are ordered by mu.
func (d *Drain) Wait(timeout time.Duration) bool {
	if d == nil {
		return true
	}
	d.mu.Lock()
	idle := d.current().idle
	d.mu.Unlock()
	select {
	case <-idle:
		return true
	default:
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-idle:
		return true
	case <-timer.C:
		return false
	}
}

// Reset starts a fresh generation. Production shutdown never resets a drain.
func (d *Drain) Reset() {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.generation = newDrainGeneration()
}

func restartFrame(retry time.Duration) []byte {
	return []byte(fmt.Sprintf("event: hub_restarting\ndata: {\"retry_after_ms\":%d}\n\n", retry.Milliseconds()))
}
