// Package screencap provides screen capture and streaming using Android's
// built-in screencap and screenrecord shell commands. No additional binaries
// are required on the device.
//
// Operations:
//  1. Screenshot: runs `adb exec-out screencap -p` to obtain a single PNG.
//  2. Stream: runs `screenrecord --output-format=h264 /dev/stdout` via
//     exec-out, forwarding the raw H.264 bitstream as byte chunks to the
//     caller. screenrecord has a built-in 3-minute limit; the stream server's
//     restart loop handles reconnection transparently.
package adbcap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"automation/src/modules/adb"
)

// pngMagic is the 8-byte signature that begins every PNG file.
var pngMagic = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}

// Manager wraps an ADB system manager and provides native screen capture
// for a specific device.
type Manager struct {
	system *adb.SystemManager
}

// New creates a Manager for the given device serial.
func New(adbManager *adb.Manager, serial string) *Manager {
	return &Manager{
		system: adb.NewSystemManager(adbManager, serial),
	}
}

// ScreenInfo returns the device display metrics (width, height, density).
func (m *Manager) ScreenInfo(ctx context.Context) (adb.ScreenInfo, error) {
	return m.system.ScreenSize(ctx)
}

// FrameContentType returns the MIME type of data produced by Stream.
// screenrecord emits a raw H.264 bitstream.
func (m *Manager) FrameContentType() string { return "video/h264" }

// Screenshot captures a single PNG frame from the device screen and writes
// it to outputPath.
func (m *Manager) Screenshot(ctx context.Context, outputPath string) error {
	data, err := m.system.RunExecOut(ctx, "screencap", "-p")
	if err != nil {
		return fmt.Errorf("screencap: %w", err)
	}

	start := bytes.Index(data, pngMagic)
	if start == -1 {
		return fmt.Errorf("screencap output does not contain a PNG magic header")
	}

	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(outputPath, data[start:], 0o644)
}

// Stream starts screenrecord in H.264 mode and forwards raw byte chunks to
// the frames channel. Each chunk is a segment of the H.264 bitstream, not a
// complete picture frame. Callers (e.g. the stream server) should concatenate
// received chunks and pipe them to clients as a continuous byte stream.
// Streaming runs until ctx is cancelled or screenrecord exits (built-in
// 3-minute limit). The caller must drain the frames channel.
func (m *Manager) Stream(ctx context.Context, frames chan<- []byte) error {
	cmd, err := m.system.ExecOutCommand(ctx,
		"screenrecord", "--output-format=h264", "/dev/stdout",
	)
	if err != nil {
		return fmt.Errorf("build screenrecord command: %w", err)
	}

	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start screenrecord: %w", err)
	}
	defer cmd.Wait() //nolint:errcheck

	return pipeChunks(ctx, pipe, frames)
}

// pipeChunks reads r in fixed-size chunks and sends each to frames until
// ctx is cancelled, an error occurs, or r reaches EOF.
func pipeChunks(ctx context.Context, r io.Reader, frames chan<- []byte) error {
	const chunkSize = 256 * 1024 // 256 KB
	buf := make([]byte, chunkSize)

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		n, readErr := r.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			select {
			case <-ctx.Done():
				return ctx.Err()
			case frames <- chunk:
			}
		}

		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return fmt.Errorf("read screenrecord: %w", readErr)
		}
	}
}
