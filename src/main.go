package main

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"automation/src/modules/adb"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	manager := adb.New(filepath.Join(".", "bin"))
	devices, err := manager.ListDevices(ctx)
	if err != nil {
		log.Fatalf("adb list devices failed: %v", err)
	}

	if len(devices) == 0 {
		fmt.Println("No devices found")
		return
	}

	for _, device := range devices {
		fmt.Printf("%s\t%s\n", device.Serial, device.State)
	}
}
