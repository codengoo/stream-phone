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
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"automation/src/modules/adb"
	"automation/src/modules/resource"
)

const (
	deviceBin  = "/data/local/tmp/minicap"
	deviceSO   = "/data/local/tmp/minicap.so"
	socketName = "ieccorp_minicap"
	cmd        = "LD_LIBRARY_PATH=/data/local/tmp /data/local/tmp/minicap -n '" + socketName + "'"
	streamPort = 1717
	bannerSize = 24
	maxAPI     = 33
	minAPI     = 10
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

// Manager wraps an ADB manager and provides minicap operations for a specific device.
type Manager struct {
	adb     *adb.Manager
	BinDir  string
	serial  string
	fetcher *resource.Downloader
	system  *adb.SystemManager
}

// New creates a Manager for the given device. cacheDir is where downloaded
// binaries are stored (e.g. "bin/minicap-cache").
func New(adbManager *adb.Manager, serial, cacheDir string) *Manager {
	return &Manager{
		adb:     adbManager,
		BinDir:  cacheDir,
		serial:  serial,
		fetcher: resource.NewDownloader(),
		system:  adb.NewSystemManager(adbManager, serial),
	}
}

// Setup downloads the minicap binary and shared library for the device,
// then pushes them onto the device if they are not already present.
func (m *Manager) Setup(ctx context.Context) error {
	// Skip push if binaries are already on the device.
	onDevice, err := m.system.FileExists(ctx, deviceBin)
	if onDevice {
		return nil
	}

	// Gather device info needed to determine which minicap binary to use.
	props, err := m.system.DeviceProps(ctx)
	if err != nil {
		return fmt.Errorf("get device props: %w", err)
	}
	api := props["ro.build.version.sdk"]
	abi := props["ro.product.cpu.abi"]

	apiInt, err := strconv.Atoi(api)
	if err != nil {
		return fmt.Errorf("parse api level %q: %w", api, err)
	}

	if apiInt > maxAPI || apiInt < minAPI {
		return fmt.Errorf("unsupported api level: %d (must be between %d and %d)", apiInt, minAPI, maxAPI)
	}

	binLocal, soLocal, err := m.ensureBinaries(ctx, apiInt, abi)
	if err != nil {
		return fmt.Errorf("ensure binaries: %w", err)
	}

	// Push files to device.
	if err := m.system.PushFile(ctx, binLocal, deviceBin); err != nil {
		return fmt.Errorf("push minicap: %w", err)
	}

	if err := m.system.PushFile(ctx, soLocal, deviceSO); err != nil {
		return fmt.Errorf("push minicap.so: %w", err)
	}

	// Make executable.
	if _, err := m.system.RunShell(ctx, "chmod", "755", deviceBin); err != nil {
		return fmt.Errorf("chmod minicap: %w", err)
	}

	return nil
}

// ScreenInfo returns the device display metrics (width, height, density).
func (m *Manager) ScreenInfo(ctx context.Context) (adb.ScreenInfo, error) {
	return m.system.ScreenSize(ctx)
}

// Screenshot captures a single JPEG frame from the device screen and writes it
// to outputPath.
func (m *Manager) Screenshot(ctx context.Context, outputPath string) error {
	// Ensure binaries are set up on the device.
	if err := m.Setup(ctx); err != nil {
		return fmt.Errorf("setup: %w", err)
	}

	// Check screen size to determine the correct minicap parameters.
	info, err := m.system.ScreenSize(ctx)
	if err != nil {
		return fmt.Errorf("screen size: %w", err)
	}

	proj := fmt.Sprintf("%dx%d@%dx%d/0", info.Width, info.Height, info.Width, info.Height)
	data, err := m.system.RunExecOut(ctx, "sh", "-c",
		fmt.Sprintf("%s -P %s -s", cmd, proj),
	)

	if err != nil {
		return fmt.Errorf("run minicap screenshot: %w", err)
	}

	start := bytes.Index(data, []byte{0xff, 0xd8})
	if start == -1 {
		return fmt.Errorf("không tìm thấy định dạng ảnh JPEG trong dữ liệu trả về")
	}
	actualImageData := data[start:]

	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(outputPath, actualImageData, 0o644)
}

// Stream starts minicap in streaming mode and sends each JPEG frame to the
// frames channel. Streaming runs until ctx is cancelled or an error occurs.
// The caller must drain the frames channel.
func (m *Manager) Stream(ctx context.Context, frames chan<- []byte) error {
	// Ensure binaries are set up on the device.
	if err := m.Setup(ctx); err != nil {
		return fmt.Errorf("setup: %w", err)
	}

	// Check screen size to determine the correct minicap parameters.
	info, err := m.system.ScreenSize(ctx)
	if err != nil {
		return fmt.Errorf("screen size: %w", err)
	}

	proj := fmt.Sprintf("%dx%d@%dx%d/0", info.Width, info.Height, info.Width, info.Height)
	shellCmd := fmt.Sprintf("%s -P %s 2>/dev/null", cmd, proj)

	// Start minicap server on device.
	serverCmd, err := m.system.ShellCommand(ctx, shellCmd)
	if err != nil {
		return fmt.Errorf("build minicap server command: %w", err)
	}
	if err := serverCmd.Start(); err != nil {
		return fmt.Errorf("start minicap server: %w", err)
	}
	defer serverCmd.Wait() //nolint:errcheck

	// Give the server a moment to create its socket.
	time.Sleep(600 * time.Millisecond)

	// Forward the abstract Unix socket to a local TCP port.
	local := fmt.Sprintf("tcp:%d", streamPort)
	abstractSocket := "localabstract:" + socketName
	if err := m.system.Forward(ctx, local, abstractSocket); err != nil {
		return fmt.Errorf("adb forward: %w", err)
	}
	defer m.system.RemoveForward(context.Background(), local)

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

func (m *Manager) ensureBinaries(ctx context.Context, api int, abi string) (binPath, soPath string, err error) {
	var binName string
	if api >= 16 {
		binName = "minicap"
	} else {
		binName = "minicap-nopie"
	}
	dir := filepath.Join(m.BinDir, "minicap")
	binPath = filepath.Join(dir, abi, binName)
	soPath = filepath.Join(dir, "minicap-shared", fmt.Sprintf("android-%d", api), abi, "minicap.so")

	// Return cached files if both exist.
	if statOK(binPath) && statOK(soPath) {
		return binPath, soPath, nil
	}

	downloadUrl := "https://ww-resources.haleinteractive.vn/nghia-dt-test/minicap.zip"
	downloadPath := filepath.Join(m.BinDir, "minicap.zip")
	if err := m.fetcher.Download(ctx, downloadUrl, downloadPath, resource.DownloadOptions{ExpectedMD5: nil, Extract: true}); err != nil {
		return "", "", fmt.Errorf("download minicap binary: %w", err)
	}

	// Double check that the expected files now exist after extraction.
	if !statOK(binPath) || !statOK(soPath) {
		return "", "", fmt.Errorf("minicap binaries not found after download and extract")
	}

	return binPath, soPath, nil
}

func statOK(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
