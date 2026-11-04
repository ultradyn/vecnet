package httpapi

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestDrainGenerationAndWait(t *testing.T) {
	d := NewDrain()
	initial := d.C()
	select {
	case <-initial:
		t.Fatal("new drain announced")
	default:
	}
	if d.RetryAfter() != DefaultRestartRetry {
		t.Fatalf("default retry = %v", d.RetryAfter())
	}
	done := d.Join()
	if d.Wait(time.Millisecond) {
		t.Fatal("wait ignored joined stream")
	}
	d.Announce(1500 * time.Millisecond)
	d.Announce(9 * time.Second)
	select {
	case <-initial:
	default:
		t.Fatal("announce did not close generation")
	}
	if d.RetryAfter() != 1500*time.Millisecond {
		t.Fatalf("repeat announce changed retry to %v", d.RetryAfter())
	}
	done()
	done()
	if !d.Wait(time.Second) {
		t.Fatal("completed stream did not leave drain")
	}
	d.Reset()
	if d.C() == initial {
		t.Fatal("reset reused generation")
	}
	select {
	case <-d.C():
		t.Fatal("reset generation already announced")
	default:
	}
	if d.RetryAfter() != DefaultRestartRetry {
		t.Fatalf("reset retry = %v", d.RetryAfter())
	}
	// A late completion of the old generation cannot complete a new stream.
	late := d.Join()
	d.Announce(0)
	if d.Wait(time.Millisecond) {
		t.Fatal("unfinished old generation completed")
	}
	d.Reset()
	next := d.Join()
	late()
	if d.Wait(time.Millisecond) {
		t.Fatal("old completion drained new generation")
	}
	next()
	if !d.Wait(time.Second) {
		t.Fatal("new generation did not finish")
	}
}

func TestDrainNilMethods(t *testing.T) {
	var d *Drain
	if d.C() != nil {
		t.Fatal("nil drain channel fires")
	}
	d.Announce(time.Second)
	d.Join()()
	if !d.Wait(0) {
		t.Fatal("nil drain wait did not finish")
	}
	d.Reset()
}

func TestDrainConcurrentJoinAnnounceWait(t *testing.T) {
	d := NewDrain()
	anchor := d.Join()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Go(func() { done := d.Join(); defer done(); <-d.C() })
	}
	d.Announce(time.Second)
	anchor()
	wg.Wait()
	if !d.Wait(time.Second) {
		t.Fatal("concurrent streams did not finish")
	}
}

func TestDrainFinishFlushesQueuedFrames(t *testing.T) {
	w := httptest.NewRecorder()
	c := startSSE(w, time.Second)
	defer c.close()
	if !c.send(context.Background(), []byte("event: ready\ndata: {}\n\n")) {
		t.Fatal("preamble send failed")
	}
	c.finish(restartFrame(1500*time.Millisecond), time.Second)
	want := "event: ready\ndata: {}\n\nevent: hub_restarting\ndata: {\"retry_after_ms\":1500}\n\n"
	if w.Body.String() != want || !w.Flushed {
		t.Fatalf("flushed bytes = %q, flushed=%v", w.Body.String(), w.Flushed)
	}
	if c.send(context.Background(), []byte("data: after\n\n")) {
		t.Fatal("finish left writer open")
	}
}

func TestDrainFinishBoundsStuckWriter(t *testing.T) {
	w := newStuckWriter()
	c := startSSE(w, time.Hour)
	defer c.close()
	for i := 0; i <= sseBuffer; i++ {
		if !c.send(context.Background(), []byte("data: queued\n\n")) {
			t.Fatal("queue failed")
		}
	}
	finished := make(chan struct{})
	start := time.Now()
	go func() { c.finish(restartFrame(time.Second), 20*time.Millisecond); close(finished) }()
	select {
	case <-finished:
		if time.Since(start) > time.Second {
			t.Fatal("finish exceeded bound")
		}
	case <-time.After(time.Second):
		t.Fatal("finish blocked behind full queue")
	}
}

func TestDrainInterruptsBackpressuredSend(t *testing.T) {
	w := newStuckWriter()
	c := startSSE(w, time.Hour)
	defer c.close()
	drain := NewDrain()
	c.draining = drain.C()
	for i := 0; i <= sseBuffer; i++ {
		if !c.send(context.Background(), []byte("data: queued\n\n")) {
			t.Fatal("queue failed")
		}
	}
	result := make(chan bool, 1)
	go func() { result <- c.send(context.Background(), []byte("data: blocked\n\n")) }()
	drain.Announce(time.Second)
	select {
	case sent := <-result:
		if sent {
			t.Fatal("send succeeded after restart")
		}
	case <-time.After(time.Second):
		t.Fatal("restart did not interrupt backpressure")
	}
}
