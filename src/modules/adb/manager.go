package adb

import (
	"bytes"
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
	Fetcher *resource.Downloader
}

func NewManager(binDir string) *Manager {
	return &Manager{
		BinDir:  binDir,
		Fetcher: resource.NewDownloader(),
	}
}

func (m *Manager) EnsureADB(ctx context.Context) (string, error) {
	adbPath := filepath.Join(m.BinDir, "platform-tools", "adb.exe")
	if _, err := os.Stat(adbPath); err == nil {
		return adbPath, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("stat adb: %w", err)
	}

	downloadURL, md5, err := platformToolsURL()
	if err != nil {
		return "", err
	}

	archivePath := filepath.Join(m.BinDir, "platform-tools.zip")
	if err := m.Fetcher.Download(ctx, downloadURL, archivePath, resource.DownloadOptions{
		Extract:     true,
		ExpectedMD5: &md5,
	}); err != nil {
		return "", fmt.Errorf("download platform-tools: %w", err)
	}

	if _, err := os.Stat(adbPath); err != nil {
		return "", fmt.Errorf("adb not found after extraction: %w", err)
	}

	return adbPath, nil
}

func (m *Manager) ListDevices(ctx context.Context) ([]Device, error) {
	output, err := m.ExecADB(ctx, "devices")
	if err != nil {
		return nil, err
	}

	return parseDevices(output), nil
}

// ExecADB runs `adb args...` and return it's output (not including stderr).
func (m *Manager) ExecADB(ctx context.Context, args ...string) ([]byte, error) {
	adbPath, err := m.EnsureADB(ctx)
	if err != nil {
		return nil, err
	}

	result, err := exec.CommandContext(ctx, adbPath, args...).Output()
	if err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			return result, fmt.Errorf("ADB error: %w: %s", err, bytes.TrimSpace(exitErr.Stderr))
		}

		return result, err
	}

	return result, nil
}

func platformToolsURL() (string, string, error) {
	switch runtime.GOOS {
	case "windows":
		return "https://dl.google.com/android/repository/platform-tools-latest-windows.zip", "b7c0e7ab72862c07b1d429b78bb389c5", nil
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
