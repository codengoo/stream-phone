// Package stream exposes a device's screen over HTTP.
//
// When the VideoSource produces image frames (FrameContentType returns
// "image/*"), the /stream endpoint delivers a multipart MJPEG response
// suitable for an <img> tag. When the source produces a video bitstream
// (e.g. "video/h264" from screenrecord), /stream pipes raw bytes so clients
// can consume it with a <video> tag or MediaSource API.
//
// Endpoints
//
//	GET /stream    — MJPEG or raw video stream
//	GET /snapshot  — latest captured frame (image sources only)
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
	"strings"
	"sync"

	"automation/src/modules/adb"
)

const (
	defaultAddr   = ":9373"
	mjpegBoundary = "mjpegframe"
)

// VideoSource is the interface that a frame source must satisfy to be used
// with Server. Both minicap.Manager and screencap.Manager implement this.
type VideoSource interface {
	Stream(ctx context.Context, frames chan<- []byte) error
	Screenshot(ctx context.Context, outputPath string) error
	ScreenInfo(ctx context.Context) (adb.ScreenInfo, error)
	FrameContentType() string
}

// Server broadcasts frames from a VideoSource over HTTP.
// A single capture stream is shared among all connected clients.
type Server struct {
	src  VideoSource
	addr string

	mu        sync.RWMutex
	subs      map[chan []byte]struct{}
	lastFrame []byte

	httpSrv *http.Server
}

// New creates a streaming server backed by src.
// addr is the listen address (e.g. ":9373"); pass "" to use the default.
func New(src VideoSource, addr string) *Server {
	if addr == "" {
		addr = defaultAddr
	}
	return &Server{
		src:  src,
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

// runStream keeps the source Stream running for the lifetime of ctx,
// restarting on non-context errors so transient device glitches self-heal.
func (s *Server) runStream(ctx context.Context, frames chan<- []byte) {
	for {
		if ctx.Err() != nil {
			return
		}
		_ = s.src.Stream(ctx, frames)
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

// handleStream dispatches to MJPEG or raw-video delivery based on the
// content type reported by the VideoSource.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(s.src.FrameContentType(), "image/") {
		s.handleMJPEGStream(w, r)
	} else {
		s.handleRawVideoStream(w, r)
	}
}

// handleMJPEGStream writes an infinite multipart MJPEG response.
func (s *Server) handleMJPEGStream(w http.ResponseWriter, r *http.Request) {
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
			hdr.Set("Content-Type", s.src.FrameContentType())
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

// handleRawVideoStream pipes raw byte chunks directly to the client.
// Suitable for H.264 or other video bitstreams produced by screenrecord.
func (s *Server) handleRawVideoStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", s.src.FrameContentType())
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ch, unsub := s.subscribe()
	defer unsub()

	for {
		select {
		case <-r.Context().Done():
			return
		case chunk, ok := <-ch:
			if !ok {
				return
			}
			if _, err := w.Write(chunk); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}
}

// handleSnapshot returns the most recently captured frame.
func (s *Server) handleSnapshot(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	frame := s.lastFrame
	s.mu.RUnlock()

	if len(frame) == 0 {
		http.Error(w, "no frame available yet — stream may still be starting", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", s.src.FrameContentType())
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(frame)))
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_, _ = w.Write(frame)
}

// handleInfo returns the device screen metrics as JSON.
func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	info, err := s.src.ScreenInfo(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_ = json.NewEncoder(w).Encode(info)
}
