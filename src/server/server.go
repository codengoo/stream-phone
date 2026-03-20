// Package server — HTTP server wiring.
//
// New builds a gin.Engine, registers all routes, and wraps it in a Server
// whose Serve method blocks until the context is cancelled.
//
// Route table:
//
//	GET /health              — liveness + resource check
//	GET /devices             — list ADB-connected devices
//	GET /:device/stream      — MJPEG or raw H.264 stream
//	GET /:device/snapshot    — latest captured frame
//	GET /:device/info        — screen metrics JSON
package server

import (
	"context"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"automation/src/modules/adb"
)

const defaultAddr = ":9373"

// Config holds all constructor parameters for Server.
type Config struct {
	// Addr is the TCP listen address, e.g. ":9373". Defaults to ":9373".
	Addr string
	// BinDir is the root directory for ADB and minicap binaries, e.g. "./bin".
	BinDir string
	// MinicapDir is the minicap cache directory, typically BinDir+"/minicap".
	MinicapDir string
	// ADBManager is the shared ADB Manager used for all device operations.
	ADBManager *adb.Manager
}

// Server is the top-level HTTP server.
type Server struct {
	addr    string
	httpSrv *http.Server
}

// New assembles and returns a Server from cfg.
func New(ctx context.Context, cfg Config) *Server {
	if cfg.Addr == "" {
		cfg.Addr = defaultAddr
	}

	sources := NewSourceCache(cfg.ADBManager, cfg.MinicapDir)
	h := NewHandlers(ctx, cfg.ADBManager, sources, cfg.MinicapDir)

	engine := gin.Default()
	engine.Use(corsHeaders())

	engine.GET("/health", h.Health)
	engine.GET("/devices", h.Devices)
	engine.GET("/device/:device/stream", h.Stream)
	engine.GET("/device/:device/snapshot", h.Snapshot)
	engine.GET("/device/:device/info", h.Info)

	return &Server{
		addr: cfg.Addr,
		httpSrv: &http.Server{
			Addr:    cfg.Addr,
			Handler: engine,
		},
	}
}

// Serve starts the HTTP server and blocks until ctx is cancelled or a fatal
// listener error occurs.
func (s *Server) Serve(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		if err := s.httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("http server: %w", err)
		}
	}()

	select {
	case <-ctx.Done():
		_ = s.httpSrv.Close()
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

// Addr returns the configured listen address.
func (s *Server) Addr() string { return s.addr }

// corsHeaders adds Access-Control-Allow-Origin: * to every response.
func corsHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Next()
	}
}
