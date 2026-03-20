package main

import (
	"context"
	"log"
	"path/filepath"

	"automation/src/modules/adb"
	"automation/src/server"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	binDir := filepath.FromSlash("./bin")
	adbMgr := adb.NewManager(filepath.Join(binDir, "adb"))

	s := server.New(ctx, server.Config{
		Addr:       ":9373",
		BinDir:     binDir,
		MinicapDir: filepath.Join(binDir, "minicap"),
		ADBManager: adbMgr,
	})

	log.Println("Server running at http://localhost:9373")

	// Serve blocks until ctx is cancelled or a fatal error occurs.
	if err := s.Serve(ctx); err != nil {
		log.Fatal(err)
	}
}
