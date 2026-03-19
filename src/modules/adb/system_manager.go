package adb

import (
	"context"
	"errors"
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
	// Lấy thông tin DisplayDeviceInfo (chứa size, density) và Orientation
	combinedCmd := "dumpsys display | grep -E 'DisplayDeviceInfo|mCurrentOrientation'"

	out, err := m.RunShell(ctx, "sh", "-c", combinedCmd)
	if err != nil {
		return ScreenInfo{}, err
	}
	output := string(out)
	println(output)

	// Parse từ dòng: DisplayDeviceInfo{"Built-in Screen": ..., 720 x 1280, ..., density 320, ...}

	// 1. Parse Size (720 x 1280)
	sizeRe := regexp.MustCompile(`(\d+)\s+x\s+(\d+)`)
	sizeMatch := sizeRe.FindStringSubmatch(output)
	w, h := 0, 0
	if len(sizeMatch) > 2 {
		w, _ = strconv.Atoi(sizeMatch[1])
		h, _ = strconv.Atoi(sizeMatch[2])
	}

	// 2. Parse Density (density 320)
	densityRe := regexp.MustCompile(`density\s+(\d+)`)
	densMatch := densityRe.FindStringSubmatch(output)
	d := 0
	if len(densMatch) > 1 {
		d, _ = strconv.Atoi(densMatch[1])
	}

	// 3. Parse Orientation (mCurrentOrientation=1)
	orientRe := regexp.MustCompile(`mCurrentOrientation=(\d)`)
	orientMatch := orientRe.FindStringSubmatch(output)
	orientation := 0
	if len(orientMatch) > 1 {
		orientation, _ = strconv.Atoi(orientMatch[1])
	}

	return ScreenInfo{
		Width:       w,
		Height:      h,
		Density:     DensityInfo{Physical: d, Current: d, Scale: 1.0}, // Đơn giản hóa vì dumpsys trả về current
		Orientation: orientation,
		Rotation:    orientation * 90,
	}, nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

var (
	sizeRegex    = regexp.MustCompile(`Override size: (\d+)x(\d+)|Physical size: (\d+)x(\d+)`)
	densityRegex = regexp.MustCompile(`Override density: (\d+)|Physical density: (\d+)`)
	orientRegex  = regexp.MustCompile(`mCurrentOrientation=(\d)|orientation=(\d)`)
)

func parseWmSize(output string) (w, h int, err error) {
	matches := sizeRegex.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return 0, 0, errors.New("size not found")
	}
	// Lấy match cuối cùng (thường là Override)
	last := matches[len(matches)-1]
	if last[1] != "" {
		w, _ = strconv.Atoi(last[1])
		h, _ = strconv.Atoi(last[2])
	} else {
		w, _ = strconv.Atoi(last[3])
		h, _ = strconv.Atoi(last[4])
	}
	return w, h, nil
}

func parseOrientation(output string) int {
	match := orientRegex.FindStringSubmatch(output)
	if len(match) > 0 {
		// Tìm group nào có dữ liệu (vì regex có toán tử OR)
		for i := 1; i < len(match); i++ {
			if match[i] != "" {
				orient, _ := strconv.Atoi(match[i])
				return orient
			}
		}
	}
	return 0
}

func parseWmDensity(output string) (DensityInfo, error) {
	var info DensityInfo

	// 1. Tìm Physical Density (Bắt buộc phải có)
	physRegex := regexp.MustCompile(`(?i)physical\s+density:\s+(\d+)`)
	physMatch := physRegex.FindStringSubmatch(output)
	if len(physMatch) > 1 {
		info.Physical, _ = strconv.Atoi(physMatch[1])
	}

	if info.Physical <= 0 {
		return info, fmt.Errorf("could not find physical density in output")
	}

	// 2. Tìm Override Density
	overRegex := regexp.MustCompile(`(?i)override\s+density:\s+(\d+)`)
	overMatch := overRegex.FindStringSubmatch(output)

	if len(overMatch) > 1 {
		// Nếu có thiết lập ghi đè
		info.Override, _ = strconv.Atoi(overMatch[1])
		info.Current = info.Override
		info.Scale = float64(info.Override) / float64(info.Physical)
	} else {
		// MẶC ĐỊNH: Nếu không có override, gán bằng physical
		info.Override = info.Physical
		info.Current = info.Physical
		info.Scale = 1.0
	}

	return info, nil
}
