// Package screencap provides screen capture and streaming using Android's
// built-in screencap shell command. No additional binaries are required on
// the device.
//
// Screencap works by:
//  1. For screenshots: running `adb exec-out screencap -p` to obtain a single
//     PNG image direct from stdout.
//  2. For streaming: running a continuous shell loop that emits back-to-back
//     PNG images, then splitting the raw byte stream on PNG magic bytes to
//     recover individual frames and forward them to the caller.
package screencap

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

// FrameContentType returns the MIME type of frames produced by Stream.
func (m *Manager) FrameContentType() string { return "image/png" }

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

// Stream continuously captures frames using a shell screencap loop and sends
// each PNG frame to the frames channel. Streaming runs until ctx is cancelled
// or an unrecoverable error occurs. The caller must drain the frames channel.
func (m *Manager) Stream(ctx context.Context, frames chan<- []byte) error {
	cmd, err := m.system.ExecOutCommand(ctx, "sh", "-c",
		"while true; do screencap -p; sleep 0.1; done",
	)
	if err != nil {
		return fmt.Errorf("build screencap stream command: %w", err)
	}

	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start screencap stream: %w", err)
	}
	defer cmd.Wait() //nolint:errcheck

	return streamPNGFrames(ctx, pipe, frames)
}

// streamPNGFrames reads a raw byte stream, splits it on PNG magic bytes, and
// sends each complete frame to frames. A frame is considered complete when the
// next PNG magic header is encountered in the stream.
func streamPNGFrames(ctx context.Context, r io.Reader, frames chan<- []byte) error {
	const readBuf = 512 * 1024 // 512 KB per read
	var acc []byte
	buf := make([]byte, readBuf)

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		n, readErr := r.Read(buf)
		if n > 0 {
			acc = append(acc, buf[:n]...)

			for {
				// Locate the start of the leading PNG in the buffer.
				start := bytes.Index(acc, pngMagic)
				if start == -1 {
					acc = nil // No magic found; discard garbage bytes.
					break
				}
				acc = acc[start:] // Trim any bytes before the PNG header.

				// The next occurrence of the magic marks the boundary of
				// the current frame.
				next := bytes.Index(acc[len(pngMagic):], pngMagic)
				if next == -1 {
					break // Frame is incomplete; wait for more data.
				}

				frameEnd := next + len(pngMagic)
				frame := make([]byte, frameEnd)
				copy(frame, acc[:frameEnd])
				acc = acc[frameEnd:]

				select {
				case <-ctx.Done():
					return ctx.Err()
				case frames <- frame:
				}
			}
		}

		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return fmt.Errorf("read screencap stream: %w", readErr)
		}
	}
}
