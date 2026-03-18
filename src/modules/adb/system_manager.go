package adb

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// ScreenInfo holds display metrics for a device.
type ScreenInfo struct {
	Width   int
	Height  int
	Density int
}

// SystemManager wraps a Manager for a specific device, providing
// file-oriented and shell-oriented device operations without requiring
// the caller to pass a serial number on every call.
type SystemManager struct {
	*Manager
	serial string
}

// NewSystemManager creates a SystemManager bound to the given device serial.
func NewSystemManager(manager *Manager, serial string) *SystemManager {
	return &SystemManager{Manager: manager, serial: serial}
}

// runADB resolves the adb binary and runs arbitrary adb arguments,
// returning combined stdout+stderr output.
func (m *SystemManager) runADB(ctx context.Context, args ...string) ([]byte, error) {
	adbPath, err := m.EnsureADB(ctx)
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, adbPath, args...).CombinedOutput()
}

// execADB runs an adb command returning stdout only, without mixing stderr.
// Use for binary output (e.g. exec-out) where stderr must not corrupt data.
func (m *SystemManager) execADB(ctx context.Context, args ...string) ([]byte, error) {
	adbPath, err := m.EnsureADB(ctx)
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, adbPath, args...).Output()
}

// FileExists reports whether a regular file exists on the target device.
func (m *SystemManager) FileExists(ctx context.Context, remotePath string) (bool, error) {
	out, err := m.RunShell(ctx, "sh", "-c",
		fmt.Sprintf("test -f %s && echo 1 || echo 0", shellQuote(remotePath)))
	if err != nil {
		return false, fmt.Errorf("check file exists %q: %w", remotePath, err)
	}
	return strings.TrimSpace(string(out)) == "1", nil
}

// PushFile copies a local file onto the device.
func (m *SystemManager) PushFile(ctx context.Context, localPath, remotePath string) error {
	out, err := m.runADB(ctx, "-s", m.serial, "push", localPath, remotePath)
	if err != nil {
		return fmt.Errorf("push %q -> %q: %w: %s", localPath, remotePath, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// PullFile copies a file from the device to the local machine.
func (m *SystemManager) PullFile(ctx context.Context, remotePath, localPath string) error {
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return fmt.Errorf("create local dir for %q: %w", localPath, err)
	}
	out, err := m.runADB(ctx, "-s", m.serial, "pull", remotePath, localPath)
	if err != nil {
		return fmt.Errorf("pull %q -> %q: %w: %s", remotePath, localPath, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ShellCommand builds an `adb shell` command for the device.
func (m *SystemManager) ShellCommand(ctx context.Context, args ...string) (*exec.Cmd, error) {
	adbPath, err := m.EnsureADB(ctx)
	if err != nil {
		return nil, err
	}
	cmdArgs := append([]string{"-s", m.serial, "shell"}, args...)
	return exec.CommandContext(ctx, adbPath, cmdArgs...), nil
}

// RunShell runs an `adb shell` command and returns its combined output.
func (m *SystemManager) RunShell(ctx context.Context, args ...string) ([]byte, error) {
	adbArgs := append([]string{"-s", m.serial, "shell"}, args...)
	out, err := m.runADB(ctx, adbArgs...)
	if err != nil {
		return nil, fmt.Errorf("adb shell %v: %w: %s", args, err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// ExecOut runs `adb exec-out args...` and returns stdout only.
// Unlike RunShell, stderr is not mixed into the output — safe for binary data.
func (m *SystemManager) ExecOut(ctx context.Context, args ...string) ([]byte, error) {
	adbArgs := append([]string{"-s", m.serial, "exec-out"}, args...)
	out, err := m.execADB(ctx, adbArgs...)
	if err != nil {
		return nil, fmt.Errorf("adb exec-out %v: %w", args, err)
	}
	return out, nil
}

// Forward creates an adb port-forward rule: adb forward local remote.
func (m *SystemManager) Forward(ctx context.Context, local, remote string) error {
	out, err := m.runADB(ctx, "-s", m.serial, "forward", local, remote)
	if err != nil {
		return fmt.Errorf("adb forward %s %s: %w: %s", local, remote, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// RemoveForward removes an adb port-forward rule. Errors are silently ignored
// because this is typically called in a defer for cleanup.
func (m *SystemManager) RemoveForward(ctx context.Context, local string) {
	_, _ = m.runADB(ctx, "-s", m.serial, "forward", "--remove", local)
}

// DeviceProp returns an Android system property value from `getprop`.
func (m *SystemManager) DeviceProp(ctx context.Context, prop string) (string, error) {
	out, err := m.RunShell(ctx, "getprop", prop)
	if err != nil {
		return "", fmt.Errorf("getprop %q: %w", prop, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// ScreenSize returns the current display metrics (size and density) for the device.
func (m *SystemManager) ScreenSize(ctx context.Context) (ScreenInfo, error) {
	sizeOut, err := m.RunShell(ctx, "wm", "size")
	if err != nil {
		return ScreenInfo{}, fmt.Errorf("wm size: %w", err)
	}
	w, h, err := parseWmSize(strings.TrimSpace(string(sizeOut)))
	if err != nil {
		return ScreenInfo{}, err
	}

	densityOut, err := m.RunShell(ctx, "wm", "density")
	if err != nil {
		return ScreenInfo{}, fmt.Errorf("wm density: %w", err)
	}
	d, err := parseWmDensity(strings.TrimSpace(string(densityOut)))
	if err != nil {
		return ScreenInfo{}, err
	}

	return ScreenInfo{Width: w, Height: h, Density: d}, nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// parseWmSize parses `wm size` output. The last "WxH" token wins
// so an override size takes precedence over the physical size.
func parseWmSize(output string) (w, h int, err error) {
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

// parseWmDensity parses `wm density` output. The last numeric value wins
// so an override density takes precedence over the physical density.
func parseWmDensity(output string) (int, error) {
	d := -1
	for _, line := range strings.Split(output, "\n") {
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		val, err := strconv.Atoi(strings.TrimSpace(line[idx+1:]))
		if err == nil {
			d = val
		}
	}
	if d < 0 {
		return 0, fmt.Errorf("could not parse screen density from %q", output)
	}
	return d, nil
}
