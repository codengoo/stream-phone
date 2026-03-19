package adb

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

type DensityInfo struct {
	Physical int     // Chỉ số DPI gốc của phần cứng
	Override int     // Chỉ số DPI do người dùng thiết lập (nếu có)
	Current  int     // Chỉ số đang được áp dụng (Ưu tiên Override)
	Scale    float64 // Tỷ lệ thu phóng (Override / Physical)
}

type ScreenInfo struct {
	Width       int
	Height      int
	Orientation int
	Rotation    int
	Density     DensityInfo
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

// ShellCommand builds an `adb shell` command for the device.
func (m *SystemManager) ShellCommand(ctx context.Context, args ...string) (*exec.Cmd, error) {
	adbPath, err := m.EnsureADB(ctx)
	if err != nil {
		return nil, err
	}
	cmdArgs := append([]string{"-s", m.serial, "shell"}, args...)
	return exec.CommandContext(ctx, adbPath, cmdArgs...), nil
}

func (m *SystemManager) RunOnDevice(ctx context.Context, args ...string) ([]byte, error) {
	adbArgs := append([]string{"-s", m.serial}, args...)
	out, err := m.Manager.ExecADB(ctx, adbArgs...)
	if err != nil {
		return out, fmt.Errorf("adb %v: %w: %s", args, err, string(out))
	}
	return out, nil
}

// RunShell run command `adb -s <serial> shell args...` and return its output.
// Trả về lệnh đã biến đổi, parse error nên nếu shell có lỗi thì cũng sẽ có lỗi tương ứng.
func (m *SystemManager) RunShell(ctx context.Context, args ...string) ([]byte, error) {
	adbArgs := append([]string{"shell"}, args...)
	return m.RunOnDevice(ctx, adbArgs...)
}

// RunExecOut run command `adb -s <serial> exec-out args...` and return its output.
// Truyền raw output (không thêm, bớt ký tự nào, không parse lỗi). Không trả lỗi nếu socket hoặc adb không lỗi. Đây là điểm khác biệt với RunShell
func (m *SystemManager) RunExecOut(ctx context.Context, args ...string) ([]byte, error) {
	adbArgs := append([]string{"exec-out"}, args...)
	return m.RunOnDevice(ctx, adbArgs...)
}

// Forward creates an adb port-forward rule: adb forward local remote.
func (m *SystemManager) Forward(ctx context.Context, local, remote string) error {
	adbArgs := []string{"forward", local, remote}
	_, err := m.RunOnDevice(ctx, adbArgs...)
	return err
}

// RemoveForward removes an adb port-forward rule. Errors are silently ignored
// because this is typically called in a defer for cleanup.
func (m *SystemManager) RemoveForward(ctx context.Context, local string) {
	adbArgs := []string{"forward", "--remove", local}
	_, _ = m.RunOnDevice(ctx, adbArgs...)
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
	adbArgs := []string{"push", localPath, remotePath}
	_, err := m.RunOnDevice(ctx, adbArgs...)
	return err
}

// PullFile copies a file from the device to the local machine.
func (m *SystemManager) PullFile(ctx context.Context, remotePath, localPath string) error {
	adbArgs := []string{"pull", remotePath, localPath}
	_, err := m.RunOnDevice(ctx, adbArgs...)
	return err
}

// DeviceProp returns an Android system property value from `getprop`.
func (m *SystemManager) DeviceProp(ctx context.Context, prop string) (string, error) {
	out, err := m.RunShell(ctx, "getprop", prop)
	if err != nil {
		return "", fmt.Errorf("getprop %q: %w", prop, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// DeviceProps returns all Android system property values from `getprop`.
func (m *SystemManager) DeviceProps(ctx context.Context) (map[string]string, error) {
	var propRegex = regexp.MustCompile(`\[(.*?)\]: \[(.*?)\]`)
	out, err := m.RunShell(ctx, "getprop")
	if err != nil {
		return nil, fmt.Errorf("getprop: %w", err)
	}

	lines := string(out)
	props := make(map[string]string)

	// Tìm tất cả các cặp khớp với format [key]: [value]
	matches := propRegex.FindAllStringSubmatch(lines, -1)
	for _, match := range matches {
		if len(match) == 3 {
			key := match[1]
			val := match[2]
			props[key] = val
		}
	}

	return props, nil
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

	rotOut, err := m.RunShell(ctx, "sh", "-c", "dumpsys", "SurfaceFlinger")
	if err != nil {
		return ScreenInfo{}, fmt.Errorf("dumpsys display: %w", err)
	}
	orientation := parseOrientation(string(rotOut))

	return ScreenInfo{Width: w, Height: h, Density: d, Orientation: orientation, Rotation: orientation * 90}, nil
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
func parseWmDensity(output string) (DensityInfo, error) {
	var info DensityInfo
	lines := strings.Split(output, "\n")

	for _, line := range lines {
		line = strings.ToLower(strings.TrimSpace(line))
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}

		// Lấy giá trị số sau dấu hai chấm
		valStr := strings.TrimSpace(line[idx+1:])
		val, err := strconv.Atoi(valStr)
		if err != nil {
			continue
		}

		// Phân loại dựa trên tiền tố của dòng
		if strings.Contains(line, "physical") {
			info.Physical = val
		} else if strings.Contains(line, "override") {
			info.Override = val
		}
	}

	// Kiểm tra tính hợp lệ
	if info.Physical <= 0 {
		return info, fmt.Errorf("could not find physical density in output: %q", output)
	}

	// Logic tính toán Current và Scale
	if info.Override > 0 {
		info.Current = info.Override
		info.Scale = float64(info.Override) / float64(info.Physical)
	} else {
		info.Current = info.Physical
		info.Scale = 1.0
	}

	return info, nil
}

// parseOrientation parses the output of `dumpsys display` to find the current screen orientation.
func parseOrientation(output string) int {
	// Regex tìm "orientation=X" hoặc "mCurrentOrientation=X"
	re := regexp.MustCompile(`orientation=(\d)`)
	match := re.FindStringSubmatch(output)
	if len(match) > 1 {
		orientation, _ := strconv.Atoi(match[1])
		return orientation
	}
	return 0 // Mặc định là 0 nếu không tìm thấy
}
