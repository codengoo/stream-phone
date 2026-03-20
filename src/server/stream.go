// Package server — device-scoped stream / snapshot / info handlers.
//
// Routes (registered in server.go):
//
//	GET /:device/stream    — MJPEG multipart or raw H.264 stream
//	GET /:device/snapshot  — most recent captured frame
//	GET /:device/info      — JSON screen metrics
//
// Capture backend is selected via the "type" query parameter:
//
//	?type=minicap   (default)
//	?type=adbcap
package server

import (
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"

	"github.com/gin-gonic/gin"
)

const mjpegBoundary = "mjpegframe"

// Stream delivers a continuous MJPEG or raw H.264 stream to the client.
//
//	GET /:device/stream?type=minicap|adbcap
func (h *Handlers) Stream(c *gin.Context) {
	b, err := h.broadcaster(c.Param("device"), captureType(c))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if strings.HasPrefix(b.Source().FrameContentType(), "image/") {
		h.streamMJPEG(c, b)
	} else {
		h.streamRaw(c, b)
	}
}

// Snapshot returns the most recently captured frame.
//
//	GET /:device/snapshot?type=minicap|adbcap
//	200 <binary image or video bytes>
//	503 if no frame has been captured yet
func (h *Handlers) Snapshot(c *gin.Context) {
	b, err := h.broadcaster(c.Param("device"), captureType(c))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	frame := b.LastFrame()
	if len(frame) == 0 {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no frame available yet — stream may still be starting"})
		return
	}

	c.Header("Cache-Control", "no-cache")
	c.Data(http.StatusOK, b.Source().FrameContentType(), frame)
}

// Info returns screen metrics (width, height, density, orientation) as JSON.
//
//	GET /:device/info?type=minicap|adbcap
//	200 { "Width": 1080, "Height": 1920, … }
func (h *Handlers) Info(c *gin.Context) {
	src, err := h.sources.Get(c.Param("device"), captureType(c))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	info, err := src.ScreenInfo(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, info)
}

// ── helpers ───────────────────────────────────────────────────────────────────

// captureType reads the "type" query parameter. Defaults to minicap.
func captureType(c *gin.Context) CaptureType {
	switch strings.ToLower(c.Query("type")) {
	case "adbcap":
		return CaptureAdbcap
	default:
		return CaptureMinicap
	}
}

func (h *Handlers) streamMJPEG(c *gin.Context, b *Broadcaster) {
	c.Header("Content-Type", "multipart/x-mixed-replace; boundary="+mjpegBoundary)
	c.Header("Cache-Control", "no-cache, no-store")
	c.Status(http.StatusOK)

	mw := multipart.NewWriter(c.Writer)
	_ = mw.SetBoundary(mjpegBoundary)

	ch, unsub := b.Subscribe()
	defer unsub()

	for {
		select {
		case <-c.Request.Context().Done():
			return
		case frame, ok := <-ch:
			if !ok {
				return
			}
			hdr := make(textproto.MIMEHeader)
			hdr.Set("Content-Type", b.Source().FrameContentType())
			hdr.Set("Content-Length", fmt.Sprintf("%d", len(frame)))
			pw, err := mw.CreatePart(hdr)
			if err != nil {
				return
			}
			if _, err := pw.Write(frame); err != nil {
				return
			}
			c.Writer.Flush()
		}
	}
}

func (h *Handlers) streamRaw(c *gin.Context, b *Broadcaster) {
	c.Header("Content-Type", b.Source().FrameContentType())
	c.Header("Cache-Control", "no-cache, no-store")
	c.Status(http.StatusOK)

	ch, unsub := b.Subscribe()
	defer unsub()

	for {
		select {
		case <-c.Request.Context().Done():
			return
		case chunk, ok := <-ch:
			if !ok {
				return
			}
			if _, err := c.Writer.Write(chunk); err != nil {
				return
			}
			c.Writer.Flush()
		}
	}
}
