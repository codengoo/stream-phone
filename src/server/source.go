// Package server — VideoSource factory.
//
// Resolves the capture method ("minicap" or "adbcap") for a given device
// serial and returns the matching VideoSource. Sources are cached per device
// so the same Manager instance is reused across requests.
package server

import (
	"context"
	"fmt"
	"sync"

	"automation/src/modules/adb"
	"automation/src/modules/video/adbcap"
	"automation/src/modules/video/minicap"
)

// CaptureType identifies the screen-capture backend.
type CaptureType string

const (
	CaptureMinicap CaptureType = "minicap"
	CaptureAdbcap  CaptureType = "adbcap"
)

// VideoSource is the interface that every capture backend must satisfy.
type VideoSource interface {
	Stream(ctx context.Context, frames chan<- []byte) error
	Screenshot(ctx context.Context, outputPath string) error
	ScreenInfo(ctx context.Context) (adb.ScreenInfo, error)
	FrameContentType() string
}

// sourceKey uniquely identifies a (serial, type) source pair.
type sourceKey struct {
	serial      string
	captureType CaptureType
}

// SourceCache keeps one VideoSource per (device, capture-type) pair alive
// for the process lifetime so goroutines and TCP forwards are not re-created
// on each request.
type SourceCache struct {
	adbMgr     *adb.Manager
	minicapDir string

	mu      sync.Mutex
	entries map[sourceKey]VideoSource
}

// NewSourceCache creates a SourceCache.
// minicapBinDir is passed to minicap.New (e.g. "./bin/minicap").
func NewSourceCache(adbMgr *adb.Manager, minicapBinDir string) *SourceCache {
	return &SourceCache{
		adbMgr:     adbMgr,
		minicapDir: minicapBinDir,
		entries:    make(map[sourceKey]VideoSource),
	}
}

// Get returns (or creates) the VideoSource for the given device+type pair.
func (c *SourceCache) Get(serial string, ct CaptureType) (VideoSource, error) {
	key := sourceKey{serial: serial, captureType: ct}

	c.mu.Lock()
	defer c.mu.Unlock()

	if src, ok := c.entries[key]; ok {
		return src, nil
	}

	var src VideoSource
	switch ct {
	case CaptureMinicap:
		src = minicap.New(c.adbMgr, serial, c.minicapDir)
	case CaptureAdbcap:
		src = adbcap.New(c.adbMgr, serial)
	default:
		return nil, fmt.Errorf("unknown capture type %q; use \"minicap\" or \"adbcap\"", ct)
	}

	c.entries[key] = src
	return src, nil
}
