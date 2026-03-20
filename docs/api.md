# API Reference

> **Maintenance rule:** Every time an endpoint is added, changed, or removed, this file **must** be updated in the same commit/PR. See `AGENTS.md` → *Documentation Sync* for details.

Base URL: `http://localhost:9373`

All responses use `Content-Type: application/json` unless noted.  
All responses include `Access-Control-Allow-Origin: *`.

---

## GET /health

Checks server liveness and verifies that required local resources (ADB, minicap) are present on disk. Downloads ADB platform-tools automatically if the binary is missing.

**Response 200 — all resources OK**

```json
{
  "server": "ok",
  "resources": [
    { "name": "adb",     "status": "ok" },
    { "name": "minicap", "status": "ok" }
  ]
}
```

**Response 503 — one or more resources unavailable**

```json
{
  "server": "ok",
  "resources": [
    { "name": "adb",     "status": "ok" },
    { "name": "minicap", "status": "missing" }
  ]
}
```

Resource `status` values:

| Value | Meaning |
|---|---|
| `ok` | Resource present on disk |
| `missing` | Binary directory / file not found |
| `error: <msg>` | Download or verification failed |

---

## GET /devices

Lists all ADB-connected devices.

**Response 200**

```json
{
  "devices": [
    { "Serial": "emulator-5554",      "State": "device" },
    { "Serial": "192.168.1.10:5555",  "State": "device" }
  ]
}
```

---

## GET /:device/stream

Starts a continuous screen stream for the given device.

**Path parameter**

| Parameter | Example | Description |
|---|---|---|
| `device` | `emulator-5554` | ADB device serial (may contain `:`) |

**Query parameter**

| Parameter | Values | Default | Description |
|---|---|---|---|
| `type` | `minicap`, `adbcap` | `minicap` | Capture backend |

**Response — minicap (MJPEG)**

```
Content-Type: multipart/x-mixed-replace; boundary=mjpegframe
```

Each part carries a JPEG frame:

```
--mjpegframe
Content-Type: image/jpeg
Content-Length: <n>

<binary jpeg>
```

Suitable for use in an `<img src="...">` tag or Electron's `BrowserWindow`.

**Response — adbcap (raw H.264)**

```
Content-Type: video/h264
```

Raw H.264 bitstream. Consume via `MediaSource` API or pipe to `ffplay`.

---

## GET /:device/snapshot

Returns the most recently captured frame (last frame from the broadcaster buffer).

**Path / query parameters:** same as `/stream`.

**Response 200**

Binary image/video bytes with the same `Content-Type` as the active backend.

**Response 503**

```json
{ "error": "no frame available yet — stream may still be starting" }
```

---

## GET /:device/info

Returns screen dimensions, orientation, and density for the given device.

**Path / query parameters:** same as `/stream`.

**Response 200**

```json
{
  "Width": 1080,
  "Height": 1920,
  "Orientation": 0,
  "Rotation": 0,
  "Density": {
    "Physical": 420,
    "Override": 0,
    "Current": 420,
    "Scale": 1
  }
}
```

---

## Error shape

All error responses share a common JSON shape:

```json
{ "error": "<human-readable message>" }
```
