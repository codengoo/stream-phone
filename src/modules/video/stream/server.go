// Package stream exposes a device's screen over HTTP so that an Electron
// renderer (or any browser) can display it with a single <img> tag.
//
// Endpoints
//
//	GET /stream    — multipart MJPEG stream; use as <img src="http://HOST/stream">
//	GET /snapshot  — single JPEG frame (latest captured)
//	GET /info      — JSON object with screen metrics
//	GET /health    — "ok" liveness probe
package stream

import (
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"sync"

	"automation/src/modules/video/minicap"
)

const (
	defaultAddr   = ":9373"
	mjpegBoundary = "mjpegframe"
)

// Server wraps a per-device minicap.Manager and broadcasts frames over HTTP.
// A single minicap stream is shared among all connected clients.
type Server struct {
	mc   *minicap.Manager
	addr string

	mu        sync.RWMutex
	subs      map[chan []byte]struct{}
	lastFrame []byte

	httpSrv *http.Server
}

// New creates a streaming server for the given device.
// addr is the listen address (e.g. ":9373"); pass "" to use the default.
func New(mc *minicap.Manager, addr string) *Server {
	if addr == "" {
		addr = defaultAddr
	}
	return &Server{
		mc:   mc,
		addr: addr,
		subs: make(map[chan []byte]struct{}),
	}
}

// Serve starts the background minicap capture loop and the HTTP server.
// It blocks until ctx is cancelled or a fatal error occurs.
func (s *Server) Serve(ctx context.Context) error {
	frames := make(chan []byte, 8)

	// Fan minicap frames out to all HTTP subscribers.
	go s.broadcast(ctx, frames)

	// Drive the minicap stream; restart automatically on transient errors.
	go s.runStream(ctx, frames)

	mux := http.NewServeMux()
	mux.HandleFunc("/stream", s.handleStream)
	mux.HandleFunc("/snapshot", s.handleSnapshot)
	mux.HandleFunc("/info", s.handleInfo)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprintln(w, "ok")
	})

	s.httpSrv = &http.Server{
		Addr:    s.addr,
		Handler: mux,
	}

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

// Addr returns the listen address (useful when using ":0" for a random port).
func (s *Server) Addr() string { return s.addr }

// ── internal helpers ──────────────────────────────────────────────────────────

// runStream keeps minicap.Stream running for the lifetime of ctx,
// restarting on non-context errors so transient device glitches self-heal.
func (s *Server) runStream(ctx context.Context, frames chan<- []byte) {
	for {
		if ctx.Err() != nil {
			return
		}
		_ = s.mc.Stream(ctx, frames)
		if ctx.Err() != nil {
			return
		}
	}
}

// broadcast fans out incoming frames to every subscribed HTTP client.
func (s *Server) broadcast(ctx context.Context, frames <-chan []byte) {
	for {
		select {
		case <-ctx.Done():
			return
		case frame, ok := <-frames:
			if !ok {
				return
			}
			s.mu.Lock()
			s.lastFrame = frame
			for ch := range s.subs {
				select {
				case ch <- frame:
				default: // slow client — drop this frame rather than block
				}
			}
			s.mu.Unlock()
		}
	}
}

// subscribe registers a new client channel and returns an unsubscribe func.
func (s *Server) subscribe() (ch chan []byte, unsub func()) {
	ch = make(chan []byte, 4)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}
}

// handleStream writes an infinite multipart MJPEG response.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary="+mjpegBoundary)
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ch, unsub := s.subscribe()
	defer unsub()

	mw := multipart.NewWriter(w)
	_ = mw.SetBoundary(mjpegBoundary)

	for {
		select {
		case <-r.Context().Done():
			return
		case frame, ok := <-ch:
			if !ok {
				return
			}
			hdr := make(textproto.MIMEHeader)
			hdr.Set("Content-Type", "image/jpeg")
			hdr.Set("Content-Length", fmt.Sprintf("%d", len(frame)))
			pw, err := mw.CreatePart(hdr)
			if err != nil {
				return
			}
			if _, err := pw.Write(frame); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}
}

// handleSnapshot returns the most recently captured JPEG frame.
func (s *Server) handleSnapshot(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	frame := s.lastFrame
	s.mu.RUnlock()

	if len(frame) == 0 {
		http.Error(w, "no frame available yet — stream may still be starting", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(frame)))
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_, _ = w.Write(frame)
}

// handleInfo returns the device screen metrics as JSON.
func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	info, err := s.mc.ScreenInfo(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_ = json.NewEncoder(w).Encode(info)
}
