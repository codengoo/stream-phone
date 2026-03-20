// Package server — GET /health handler.
//
// Health checks:
//  1. Server process is alive.
//  2. ADB platform-tools are present on disk (downloads if missing).
//  3. Minicap binary directory exists locally.
package server

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/gin-gonic/gin"
)

// Health checks server liveness and local resource availability.
//
//	GET /health
//	200 { "server": "ok", "resources": [ { "name": "adb",     "status": "ok" },
//	                                      { "name": "minicap", "status": "ok" } ] }
//	503 same shape but a resource has "status": "missing" or "status": "error: ..."
func (h *Handlers) Health(c *gin.Context) {
	type resource struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}

	resources := make([]resource, 0, 2)
	hasError := false

	// ── ADB ────────────────────────────────────────────────────────────────
	adbStatus := "ok"
	if _, err := h.adbMgr.EnsureADB(c.Request.Context()); err != nil {
		adbStatus = "error: " + err.Error()
		hasError = true
	}
	resources = append(resources, resource{Name: "adb", Status: adbStatus})

	// ── minicap ────────────────────────────────────────────────────────────
	minicapStatus := "ok"
	if _, err := os.Stat(filepath.Join(h.minicapDir, "minicap")); os.IsNotExist(err) {
		minicapStatus = "missing"
		hasError = true
	}
	resources = append(resources, resource{Name: "minicap", Status: minicapStatus})

	// ── response ───────────────────────────────────────────────────────────
	status := http.StatusOK
	if hasError {
		status = http.StatusServiceUnavailable
	}
	c.JSON(status, gin.H{
		"server":    "ok",
		"resources": resources,
	})
}
