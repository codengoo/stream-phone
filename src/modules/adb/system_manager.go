package adb

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SystemManager extends Manager with common file-oriented device operations.
// Go does not support classical inheritance, so Manager is embedded instead.
type SystemManager struct {
	*Manager
}

func NewSystemManager(manager *Manager) *SystemManager {
	return &SystemManager{Manager: manager}
}

// FileExists reports whether a regular file exists on the target device.
func (m *SystemManager) FileExists(ctx context.Context, serial, remotePath string) (bool, error) {
	adbPath, err := m.EnsureADB(ctx)
	if err != nil {
		return false, err
	}

	cmd := exec.CommandContext(
		ctx,
		adbPath,
		"-s", serial,
		"shell",
		"sh", "-c",
		fmt.Sprintf("test -f %s", shellQuote(remotePath)),
	)

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return false, nil
		}
		return false, fmt.Errorf("check file exists %q: %w", remotePath, err)
	}

	return true, nil
}

// PushFile copies a local file onto the device.
func (m *SystemManager) PushFile(ctx context.Context, serial, localPath, remotePath string) error {
	adbPath, err := m.EnsureADB(ctx)
	if err != nil {
		return err
	}

	out, err := exec.CommandContext(
		ctx,
		adbPath,
		"-s", serial,
		"push",
		localPath,
		remotePath,
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("push %q -> %q: %w: %s", localPath, remotePath, err, strings.TrimSpace(string(out)))
	}

	return nil
}

// PullFile copies a file from the device to the local machine.
func (m *SystemManager) PullFile(ctx context.Context, serial, remotePath, localPath string) error {
	adbPath, err := m.EnsureADB(ctx)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return fmt.Errorf("create local dir for %q: %w", localPath, err)
	}

	out, err := exec.CommandContext(
		ctx,
		adbPath,
		"-s", serial,
		"pull",
		remotePath,
		localPath,
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("pull %q -> %q: %w: %s", remotePath, localPath, err, strings.TrimSpace(string(out)))
	}

	return nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
