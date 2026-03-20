// Package server — Handlers struct.
//
// Handlers holds all shared dependencies for HTTP route handlers and is the
// single receiver type used across health.go, devices.go and stream.go.
package server

import (
	"context"
	"sync"

	"automation/src/modules/adb"
)

// Handlers carries the runtime state shared by all HTTP handlers.
type Handlers struct {
	adbMgr     *adb.Manager
	sources    *SourceCache
	minicapDir string

	mu           sync.Mutex
	broadcasters map[sourceKey]*Broadcaster
	baseCtx      context.Context
}

// NewHandlers creates a Handlers instance.
func NewHandlers(ctx context.Context, adbMgr *adb.Manager, sources *SourceCache, minicapDir string) *Handlers {
	return &Handlers{
		adbMgr:       adbMgr,
		sources:      sources,
		minicapDir:   minicapDir,
		broadcasters: make(map[sourceKey]*Broadcaster),
		baseCtx:      ctx,
	}
}

// broadcaster returns (or lazily creates) the Broadcaster for the given
// (serial, capture-type) pair.
func (h *Handlers) broadcaster(serial string, ct CaptureType) (*Broadcaster, error) {
	key := sourceKey{serial: serial, captureType: ct}

	h.mu.Lock()
	defer h.mu.Unlock()

	if b, ok := h.broadcasters[key]; ok {
		return b, nil
	}

	src, err := h.sources.Get(serial, ct)
	if err != nil {
		return nil, err
	}

	b := NewBroadcaster(h.baseCtx, src)
	h.broadcasters[key] = b
	return b, nil
}
