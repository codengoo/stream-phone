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

func test() {
	ctx := context.Background()
	client := adb.NewManager("./bin/adb")
	mnc := minicap.New(client, "emulator-5554", "./bin/minicap")
	// mnc.Screenshot(ctx, "out/test.jpg")

	// b, _ := json.MarshalIndent(info, "", "  ")
	// fmt.Println(string(b))

	fmt.Println("\n=== Stream demo (10 frames) ===")
	streamCtx, streamCancel := context.WithCancel(ctx)
	frames := make(chan []byte, 4)

	streamErr := make(chan error, 1)
	go func() {
		streamErr <- mnc.Stream(streamCtx, frames)
	}()

	outDir := "out/frames"
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

func main() {
	test()
}
