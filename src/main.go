package main

import (
	"automation/src/modules/adb"
	"automation/src/modules/video/minicap"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

func dosmt() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	binDir := filepath.Join(".", "bin")
	adbManager := adb.NewManager(binDir)

	// ── List devices ─────────────────────────────────────────────────────────
	devices, err := adbManager.ListDevices(ctx)
	if err != nil {
		log.Fatalf("adb list devices: %v", err)
	}
	if len(devices) == 0 {
		fmt.Println("No devices found – connect a device and try again.")
		return
	}

	fmt.Println("=== Devices ===")
	for _, d := range devices {
		fmt.Printf("  %s\t%s\n", d.Serial, d.State)
	}

	// Use the first online device.
	serial := ""
	for _, d := range devices {
		if d.State == "device" {
			serial = d.Serial
			break
		}
	}
	if serial == "" {
		fmt.Println("No device in 'device' state found.")
		return
	}
	fmt.Printf("\nUsing device: %s\n\n", serial)

	mc := minicap.New(adbManager, serial, filepath.Join(binDir, "minicap-cache"))

	// ── Demo 1: Screenshot ───────────────────────────────────────────────────
	fmt.Println("=== Screenshot demo ===")
	screenshotPath := "screenshot.jpg"
	if err := mc.Screenshot(ctx, screenshotPath); err != nil {
		log.Printf("Screenshot failed: %v", err)
	} else {
		info, _ := os.Stat(screenshotPath)
		fmt.Printf("  Saved %s (%d bytes)\n", screenshotPath, info.Size())
	}

	// ── Demo 2: Stream (capture 10 frames) ───────────────────────────────────
	fmt.Println("\n=== Stream demo (10 frames) ===")
	streamCtx, streamCancel := context.WithCancel(ctx)
	frames := make(chan []byte, 4)

	streamErr := make(chan error, 1)
	go func() {
		streamErr <- mc.Stream(streamCtx, frames)
	}()

	outDir := "frames"
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		log.Fatalf("create frames dir: %v", err)
	}

	const maxFrames = 10
	count := 0
	timeout := time.After(15 * time.Second)

loop:
	for {
		select {
		case frame, ok := <-frames:
			if !ok {
				break loop
			}
			count++
			framePath := filepath.Join(outDir, fmt.Sprintf("frame_%02d.jpg", count))
			if err := os.WriteFile(framePath, frame, 0o644); err != nil {
				log.Printf("  write frame %d: %v", count, err)
			} else {
				fmt.Printf("  Frame %d: %d bytes → %s\n", count, len(frame), framePath)
			}
			if count >= maxFrames {
				break loop
			}
		case <-timeout:
			fmt.Println("  Timeout waiting for frames.")
			break loop
		}
	}

	streamCancel()
	if err := <-streamErr; err != nil && err != context.Canceled {
		log.Printf("Stream error: %v", err)
	}

	fmt.Printf("\nDone. Captured %d frame(s).\n", count)
}

func test() {
	ctx := context.Background()
	md := adb.NewSystemManager(adb.NewManager("./bin"), "emulator-5554")
	// isOk, _ := md.FileExists(ctx, "emulator-5554", "/system/bin/minicap")
	info, err := md.ScreenSize(ctx)
	if err != nil {
		fmt.Printf("Error getting screen size: %v\n", err)
		return
	}
	fmt.Printf("Screen size: %dx%d density:%d\n", info.Width, info.Height, info.Density)
}

func main() {
	test()
}
