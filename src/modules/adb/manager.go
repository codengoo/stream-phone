package adb

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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

func New(binDir string) *Manager {
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

	downloadURL, err := platformToolsURL()
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(m.BinDir, 0o755); err != nil {
		return "", fmt.Errorf("create bin dir: %w", err)
	}

	archiveData, err := m.Fetcher.Download(ctx, downloadURL)
	if err != nil {
		return "", fmt.Errorf("download platform-tools: %w", err)
	}

	if err := extractADB(ctx, m.Fetcher, bytes.NewReader(archiveData), int64(len(archiveData)), m.BinDir); err != nil {
		return "", err
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

func (m *Manager) adbPath() string {
	fileName := "adb"
	if runtime.GOOS == "windows" {
		fileName += ".exe"
	}
	return filepath.Join(m.BinDir, fileName)
}

func platformToolsURL() (string, error) {
	switch runtime.GOOS {
	case "windows":
		return "https://dl.google.com/android/repository/platform-tools-latest-windows.zip", nil
	case "linux":
		return "https://dl.google.com/android/repository/platform-tools-latest-linux.zip", nil
	case "darwin":
		return "https://dl.google.com/android/repository/platform-tools-latest-darwin.zip", nil
	default:
		return "", fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

func extractADB(ctx context.Context, fetcher *resource.Fetcher, readerAt io.ReaderAt, size int64, binDir string) error {
	zr, err := zip.NewReader(readerAt, size)
	if err != nil {
		return fmt.Errorf("open platform-tools archive: %w", err)
	}

	extracted := false
	for _, file := range zr.File {
		name := filepath.Base(file.Name)
		if !isADBArtifact(name) {
			continue
		}

		if err := extractZipFile(ctx, fetcher, file, filepath.Join(binDir, name)); err != nil {
			return err
		}
		extracted = true
	}

	if !extracted {
		return errors.New("adb files were not found in platform-tools archive")
	}

	return nil
}

func isADBArtifact(name string) bool {
	switch name {
	case "adb", "adb.exe", "AdbWinApi.dll", "AdbWinUsbApi.dll":
		return true
	default:
		return false
	}
}

func extractZipFile(ctx context.Context, fetcher *resource.Fetcher, file *zip.File, destination string) error {
	src, err := file.Open()
	if err != nil {
		return fmt.Errorf("open archive entry %s: %w", file.Name, err)
	}
	defer src.Close()

	content, err := io.ReadAll(src)
	if err != nil {
		return fmt.Errorf("read archive entry %s: %w", file.Name, err)
	}

	if err := fetcher.EnsureFile(ctx, destination, func(context.Context) ([]byte, error) {
		return content, nil
	}); err != nil {
		return fmt.Errorf("extract %s: %w", destination, err)
	}

	if err := os.Chmod(destination, file.Mode()); err != nil {
		return fmt.Errorf("chmod %s: %w", destination, err)
	}

	return nil
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
