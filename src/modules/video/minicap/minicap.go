// Package minicap provides screen capture and streaming via the Minicap tool
// (https://github.com/DeviceFarmer/minicap).
//
// Minicap works by:
//  1. Pushing a native minicap binary + shared library onto the device.
//  2. For screenshots: running minicap with the -s flag so it writes a single
//     JPEG to stdout, captured via `adb exec-out`.
//  3. For streaming: running minicap as a server that broadcasts JPEG frames
//     over an abstract Unix domain socket, then forwarding that socket to a
//     local TCP port via `adb forward`.
package minicap

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"automation/src/modules/adb"
	"automation/src/modules/resource"
)

const (
	deviceBin  = "/data/local/tmp/minicap"
	deviceSO   = "/data/local/tmp/minicap.so"
	streamPort = 1717
	bannerSize = 24
	maxAPI     = 29 // minicap prebuilts are available up to API 29
)

// Banner is the global header sent once by minicap when a streaming client
// first connects.
type Banner struct {
	Version       uint8
	HeaderSize    uint8
	Pid           uint32
	RealWidth     uint32
	RealHeight    uint32
	VirtualWidth  uint32
	VirtualHeight uint32
	Orientation   uint8
	Quirks        uint8
}

// Manager wraps an ADB manager and provides minicap operations.
type Manager struct {
	ADB      *adb.Manager
	CacheDir string // local directory used to cache downloaded minicap binaries
	fetcher  *resource.Fetcher
}

// New creates a Manager. cacheDir is where downloaded binaries are stored
// (e.g. "bin/minicap-cache").
func New(adbManager *adb.Manager, cacheDir string) *Manager {
	return &Manager{
		ADB:      adbManager,
		CacheDir: cacheDir,
		fetcher:  resource.NewFetcher(),
	}
}

// Setup downloads the minicap binary and shared library for the given device,
// then pushes them onto the device if they are not already present.
func (m *Manager) Setup(ctx context.Context, serial string) error {
	adbPath, err := m.ADB.EnsureADB(ctx)
	if err != nil {
		return err
	}

	// Skip push if binaries are already on the device.
	if m.isOnDevice(ctx, adbPath, serial) {
		return nil
	}

	api, err := m.deviceProp(ctx, adbPath, serial, "ro.build.version.sdk")
	if err != nil {
		return fmt.Errorf("get api level: %w", err)
	}

	abi, err := m.deviceProp(ctx, adbPath, serial, "ro.product.cpu.abi")
	if err != nil {
		return fmt.Errorf("get abi: %w", err)
	}

	apiInt, err := strconv.Atoi(api)
	if err != nil {
		return fmt.Errorf("parse api level %q: %w", api, err)
	}
	if apiInt > maxAPI {
		apiInt = maxAPI
	}

	binLocal, soLocal, err := m.ensureBinaries(ctx, apiInt, abi)
	fmt.Println(binLocal)
	fmt.Println(soLocal)
	if err != nil {
		return fmt.Errorf("ensure binaries: %w", err)
	}

	if err := m.push(ctx, adbPath, serial, binLocal, deviceBin); err != nil {
		return fmt.Errorf("push minicap: %w", err)
	}
	if err := m.push(ctx, adbPath, serial, soLocal, deviceSO); err != nil {
		return fmt.Errorf("push minicap.so: %w", err)
	}

	// Make executable.
	if err := m.shell(ctx, adbPath, serial, "chmod", "755", deviceBin); err != nil {
		return fmt.Errorf("chmod minicap: %w", err)
	}

	return nil
}

// Screenshot captures a single JPEG frame from the device screen and writes it
// to outputPath.
func (m *Manager) Screenshot(ctx context.Context, serial, outputPath string) error {
	if err := m.Setup(ctx, serial); err != nil {
		return fmt.Errorf("setup: %w", err)
	}

	adbPath, err := m.ADB.EnsureADB(ctx)
	if err != nil {
		return err
	}

	w, h, err := m.screenSize(ctx, adbPath, serial)
	if err != nil {
		return fmt.Errorf("screen size: %w", err)
	}

	proj := fmt.Sprintf("%dx%d@%dx%d/0", w, h, w, h)
	cmd := exec.CommandContext(ctx, adbPath,
		"-s", serial,
		"exec-out",
		"sh", "-c",
		fmt.Sprintf("LD_LIBRARY_PATH=/data/local/tmp /data/local/tmp/minicap -P %s -s", proj),
	)

	data, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("run minicap screenshot: %w", err)
	}
	if len(data) == 0 {
		return fmt.Errorf("minicap returned empty output – check device logs")
	}

	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(outputPath, data, 0o644)
}

// Stream starts minicap in streaming mode and sends each JPEG frame to the
// frames channel. Streaming runs until ctx is cancelled or an error occurs.
// The caller must drain the frames channel.
func (m *Manager) Stream(ctx context.Context, serial string, frames chan<- []byte) error {
	if err := m.Setup(ctx, serial); err != nil {
		return fmt.Errorf("setup: %w", err)
	}

	adbPath, err := m.ADB.EnsureADB(ctx)
	if err != nil {
		return err
	}

	w, h, err := m.screenSize(ctx, adbPath, serial)
	if err != nil {
		return fmt.Errorf("screen size: %w", err)
	}

	proj := fmt.Sprintf("%dx%d@%dx%d/0", w, h, w, h)
	shellCmd := fmt.Sprintf("LD_LIBRARY_PATH=/data/local/tmp /data/local/tmp/minicap -P %s 2>/dev/null", proj)

	// Start minicap server on device.
	serverCmd := exec.CommandContext(ctx, adbPath, "-s", serial, "shell", shellCmd)
	if err := serverCmd.Start(); err != nil {
		return fmt.Errorf("start minicap server: %w", err)
	}
	defer serverCmd.Wait() //nolint:errcheck

	// Give the server a moment to create its socket.
	time.Sleep(600 * time.Millisecond)

	// Forward the abstract Unix socket to a local TCP port.
	fwdOut, err := exec.CommandContext(ctx, adbPath,
		"-s", serial, "forward",
		fmt.Sprintf("tcp:%d", streamPort),
		"localabstract:minicap",
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("adb forward: %w: %s", err, strings.TrimSpace(string(fwdOut)))
	}
	defer exec.Command(adbPath, "-s", serial, "forward", "--remove", //nolint:errcheck
		fmt.Sprintf("tcp:%d", streamPort)).Run()

	// Connect to the forwarded port.
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", streamPort), 5*time.Second)
	if err != nil {
		return fmt.Errorf("connect minicap: %w", err)
	}
	defer conn.Close()

	// Read and parse the global banner (24 bytes).
	banner, err := readBanner(conn)
	if err != nil {
		return fmt.Errorf("read banner: %w", err)
	}
	_ = banner // caller can obtain it via a future BannerStream variant if needed

	// Read frames until context is cancelled.
	sizeBuf := make([]byte, 4)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if _, err := io.ReadFull(conn, sizeBuf); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("read frame size: %w", err)
		}

		frameSize := binary.LittleEndian.Uint32(sizeBuf)
		frameData := make([]byte, frameSize)
		if _, err := io.ReadFull(conn, frameData); err != nil {
			return fmt.Errorf("read frame body: %w", err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case frames <- frameData:
		}
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func readBanner(r io.Reader) (Banner, error) {
	buf := make([]byte, bannerSize)
	if _, err := io.ReadFull(r, buf); err != nil {
		return Banner{}, err
	}
	return Banner{
		Version:       buf[0],
		HeaderSize:    buf[1],
		Pid:           binary.LittleEndian.Uint32(buf[2:6]),
		RealWidth:     binary.LittleEndian.Uint32(buf[6:10]),
		RealHeight:    binary.LittleEndian.Uint32(buf[10:14]),
		VirtualWidth:  binary.LittleEndian.Uint32(buf[14:18]),
		VirtualHeight: binary.LittleEndian.Uint32(buf[18:22]),
		Orientation:   buf[22],
		Quirks:        buf[23],
	}, nil
}

func (m *Manager) isOnDevice(ctx context.Context, adbPath, serial string) bool {
	cmd := exec.CommandContext(ctx, adbPath, "-s", serial, "shell",
		fmt.Sprintf("test -f %s && echo yes", deviceBin))
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) == "yes"
}

func (m *Manager) deviceProp(ctx context.Context, adbPath, serial, prop string) (string, error) {
	out, err := exec.CommandContext(ctx, adbPath, "-s", serial, "shell", "getprop", prop).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (m *Manager) screenSize(ctx context.Context, adbPath, serial string) (w, h int, err error) {
	out, err := exec.CommandContext(ctx, adbPath, "-s", serial, "shell", "wm", "size").Output()
	if err != nil {
		return 0, 0, err
	}
	return parseSize(strings.TrimSpace(string(out)))
}

// parseSize parses `wm size` output. The last "WxH" token wins (handles
// "Override size:" taking precedence over "Physical size:").
func parseSize(output string) (w, h int, err error) {
	w, h = -1, -1
	for _, line := range strings.Split(output, "\n") {
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		sizeStr := strings.TrimSpace(line[idx+1:])
		parts := strings.SplitN(sizeStr, "x", 2)
		if len(parts) != 2 {
			continue
		}
		pw, e1 := strconv.Atoi(parts[0])
		ph, e2 := strconv.Atoi(strings.TrimSpace(parts[1]))
		if e1 == nil && e2 == nil {
			w, h = pw, ph
		}
	}
	if w < 0 {
		return 0, 0, fmt.Errorf("could not parse screen size from %q", output)
	}
	return w, h, nil
}

func (m *Manager) ensureBinaries(ctx context.Context, api int, abi string) (binPath, soPath string, err error) {
	dir := filepath.Join(m.CacheDir, fmt.Sprintf("android-%d", api), abi)
	binPath = filepath.Join(dir, "minicap")
	soPath = filepath.Join(dir, "minicap.so")

	// Return cached files if both exist.
	if statOK(binPath) && statOK(soPath) {
		return binPath, soPath, nil
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}

	base := fmt.Sprintf(
		"https://raw.githubusercontent.com/DeviceFarmer/minicap/master/dist/ndk/android-%d/%s",
		api, abi,
	)

	if err := m.fetcher.Download(ctx, base+"/minicap", binPath, resource.DownloadOptions{}); err != nil {
		return "", "", fmt.Errorf("download minicap binary: %w", err)
	}
	if err := m.fetcher.Download(ctx, base+"/minicap.so", soPath, resource.DownloadOptions{}); err != nil {
		return "", "", fmt.Errorf("download minicap.so: %w", err)
	}

	return binPath, soPath, nil
}

func (m *Manager) push(ctx context.Context, adbPath, serial, local, remote string) error {
	out, err := exec.CommandContext(ctx, adbPath, "-s", serial, "push", local, remote).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (m *Manager) shell(ctx context.Context, adbPath, serial string, args ...string) error {
	cmdArgs := append([]string{"-s", serial, "shell"}, args...)
	out, err := exec.CommandContext(ctx, adbPath, cmdArgs...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func statOK(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
