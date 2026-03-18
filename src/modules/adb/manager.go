package adb

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"automation/src/modules/resource"
)

type Device struct {
	Serial string
	State  string
}

type Manager struct {
	BinDir  string
	Fetcher *resource.Fetcher
}

func NewManager(binDir string) *Manager {
	return &Manager{
		BinDir:  binDir,
		Fetcher: resource.NewFetcher(),
	}
}

func (m *Manager) EnsureADB(ctx context.Context) (string, error) {
	adbPath := m.adbPath()
	if _, err := os.Stat(adbPath); err == nil {
		return adbPath, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("stat adb: %w", err)
	}

	downloadURL, md5, err := platformToolsURL()
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(m.BinDir, 0o755); err != nil {
		return "", fmt.Errorf("create bin dir: %w", err)
	}

	archivePath := filepath.Join(m.BinDir, "platform-tools.zip")
	if err := m.Fetcher.Download(ctx, downloadURL, archivePath, resource.DownloadOptions{
		Extract:     true,
		ExpectedMD5: md5,
	}); err != nil {
		return "", fmt.Errorf("download platform-tools: %w", err)
	}

	if _, err := os.Stat(adbPath); err != nil {
		return "", fmt.Errorf("adb not found after extraction: %w", err)
	}

	return adbPath, nil
}

func (m *Manager) ListDevices(ctx context.Context) ([]Device, error) {
	adbPath, err := m.EnsureADB(ctx)
	if err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, adbPath, "devices")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("run adb devices: %w: %s", err, strings.TrimSpace(string(output)))
	}

	return parseDevices(output), nil
}

func (m *Manager) ExecADB(ctx context.Context, args ...string) ([]byte, error) {
	adbPath, err := m.EnsureADB(ctx)
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, adbPath, args...).Output()
}

func (m *Manager) adbPath() string {
	fileName := "adb.exe"
	return filepath.Join(m.BinDir, "platform-tools", fileName)
}

func platformToolsURL() (string, string, error) {
	switch runtime.GOOS {
	case "windows":
		return "https://dl.google.com/android/repository/platform-tools-latest-windows.zip", "b7c0e7ab72862c07b1d429b78bb389c5", nil
	case "linux":
		return "https://dl.google.com/android/repository/platform-tools-latest-linux.zip", "", nil
	case "darwin":
		return "https://dl.google.com/android/repository/platform-tools-latest-darwin.zip", "", nil
	default:
		return "", "", fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

func parseDevices(output []byte) []Device {
	lines := strings.Split(string(output), "\n")
	devices := make([]Device, 0, len(lines))

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "List of devices attached") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		devices = append(devices, Device{
			Serial: fields[0],
			State:  fields[1],
		})
	}

	return devices
}
