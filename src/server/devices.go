// Package server — GET /devices handler.
package server

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Devices lists all ADB-connected devices.
//
//	GET /devices
//	200 { "devices": [ { "Serial": "emulator-5554", "State": "device" }, … ] }
func (h *Handlers) Devices(c *gin.Context) {
	devices, err := h.adbMgr.ListDevices(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list devices: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"devices": devices})
}
