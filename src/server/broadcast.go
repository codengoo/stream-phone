// Package server — per-device streaming broadcaster.
//
// A Broadcaster fans out frames from a single VideoSource to all currently
// connected HTTP clients for that device. One Broadcaster is created per
// (device, capture-type) pair on first use and kept alive until the process
// exits.
package server

import (
	"context"
	"sync"
)

// Broadcaster distributes frames from a VideoSource to HTTP client goroutines.
type Broadcaster struct {
	src VideoSource

	mu        sync.RWMutex
	subs      map[chan []byte]struct{}
	lastFrame []byte
}

// NewBroadcaster creates a Broadcaster backed by src and starts the capture
// goroutine. Capture restarts automatically on transient errors.
func NewBroadcaster(ctx context.Context, src VideoSource) *Broadcaster {
	b := &Broadcaster{
		src:  src,
		subs: make(map[chan []byte]struct{}),
	}
	raw := make(chan []byte, 8)
	go b.runSource(ctx, raw)
	go b.fanout(ctx, raw)
	return b
}

// Subscribe registers a client and returns a frame channel and an unsubscribe
// function. The caller must eventually call the unsubscribe function.
func (b *Broadcaster) Subscribe() (ch chan []byte, unsub func()) {
	ch = make(chan []byte, 4)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

// LastFrame returns the most recently received frame (nil if none yet).
func (b *Broadcaster) LastFrame() []byte {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.lastFrame
}

// Source returns the underlying VideoSource.
func (b *Broadcaster) Source() VideoSource { return b.src }

// runSource keeps src.Stream alive for the lifetime of ctx.
func (b *Broadcaster) runSource(ctx context.Context, out chan<- []byte) {
	for {
		if ctx.Err() != nil {
			return
		}
		_ = b.src.Stream(ctx, out)
		if ctx.Err() != nil {
			return
		}
	}
}

// fanout reads frames from in and pushes them to every subscriber.
func (b *Broadcaster) fanout(ctx context.Context, in <-chan []byte) {
	for {
		select {
		case <-ctx.Done():
			return
		case frame, ok := <-in:
			if !ok {
				return
			}
			b.mu.Lock()
			b.lastFrame = frame
			for ch := range b.subs {
				select {
				case ch <- frame:
				default: // drop frame for slow clients
				}
			}
			b.mu.Unlock()
		}
	}
}
