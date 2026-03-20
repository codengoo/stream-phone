# Project Structure

## Overview

`automation/streaming` is a Windows-only Go HTTP server that streams an Android device's screen over HTTP using either **minicap** (JPEG frames over TCP) or **adbcap** (raw H.264 via `screenrecord`). Multiple devices are supported simultaneously; each `(device, backend)` pair gets its own broadcaster goroutine.

---

## Directory Tree

```
automation/streaming/
├── AGENTS.md               Agent / code-review rules for this repo
├── go.mod
│
├── bin/                    Pre-bundled platform binaries (not Go source)
│   ├── adb/                ADB platform-tools (downloaded on first /health call)
│   │   └── platform-tools/
│   │       └── adb.exe
│   └── minicap/            Minicap binaries for all ABI / API combinations
│       ├── <abi>/          e.g. arm64-v8a/, armeabi-v7a/, x86_64/ …
│       │   ├── minicap
│       │   └── minicap-nopie
│       └── minicap-shared/
│           └── android-<api>/<abi>/minicap.so
│
├── docs/
│   ├── api.md              ← Canonical HTTP API reference (keep in sync)
│   ├── project.md          ← This file
│   ├── electron-stream-api.md
│   └── electron-stream-integration.md
│
└── src/
    ├── main.go             Entry point — builds Config, calls server.New + Serve
    │
    ├── modules/            Pure-logic modules; no HTTP concerns
    │   ├── adb/
    │   │   ├── manager.go        ADB binary lifecycle, device listing, ExecADB
    │   │   └── system_manager.go Device-bound shell/exec-out, forward, push/pull
    │   │
    │   ├── resource/
    │   │   └── downloader.go     HTTP download, MD5 verify, ZIP extraction
    │   │
    │   └── video/
    │       ├── adbcap/
    │       │   └── adbcap.go     Screen capture via screencap / screenrecord
    │       └── minicap/
    │           └── minicap.go    Screen capture via minicap binary on device
    │
    └── server/             HTTP server — one file per responsibility
        ├── server.go       Gin engine, route table, Serve()
        ├── handlers.go     Handlers struct, constructor, broadcaster() helper
        ├── health.go       GET /health
        ├── devices.go      GET /devices
        ├── stream.go       GET /:device/stream|snapshot|info + MJPEG/raw helpers
        ├── broadcast.go    Per-(device,type) frame broadcaster (fan-out)
        └── source.go       VideoSource interface, SourceCache, CaptureType
```

---

## Module Responsibilities

### `src/main.go`
Wires `adb.Manager`, builds `server.Config`, and calls `server.New + Serve`. No business logic.

### `src/modules/adb/manager.go`
Owns the ADB binary path. Downloads platform-tools on first use. Provides `ListDevices` and `ExecADB`.

### `src/modules/adb/system_manager.go`
Device-bound wrapper over `Manager`. Provides shell commands, `exec-out`, port forwarding, file push/pull, `getprop`, and `ScreenSize`.

### `src/modules/resource/downloader.go`
HTTP download with optional MD5 verification and ZIP extraction. Stateless; no global state.

### `src/modules/video/adbcap/adbcap.go`
Implements `VideoSource` using Android's built-in `screencap` (PNG) and `screenrecord --output-format=h264`. No extra binaries required on the device.

### `src/modules/video/minicap/minicap.go`
Implements `VideoSource` using the Minicap project. Pushes the binary + shared library to the device on first use, then streams JPEG frames over a forwarded TCP socket.

### `src/server/server.go`
Creates the Gin engine, registers the route table, and wraps it in a `Server` whose `Serve` method manages the HTTP listener lifecycle.

### `src/server/handlers.go`
`Handlers` struct that carries runtime dependencies (`adb.Manager`, `SourceCache`, broadcaster map) shared by all handler files in this package.

### `src/server/health.go`
`Handlers.Health` — checks ADB binary and local minicap directory.

### `src/server/devices.go`
`Handlers.Devices` — proxies `adb.Manager.ListDevices`.

### `src/server/stream.go`
`Handlers.Stream`, `Snapshot`, `Info` — per-device media delivery. Handles both MJPEG multipart and raw H.264.

### `src/server/broadcast.go`
`Broadcaster` — wraps a `VideoSource`, runs the capture goroutine, keeps `lastFrame`, and fans out frames to all subscribed HTTP clients via channels. One instance per `(serial, CaptureType)` pair.

### `src/server/source.go`
`VideoSource` interface, `CaptureType` constants, and `SourceCache` which lazily constructs and caches one `VideoSource` per `(serial, CaptureType)`.

---

## Data Flow

```
Device (Android)
      │  ADB / TCP
      ▼
VideoSource (minicap.Manager or adbcap.Manager)
      │  []byte frames
      ▼
Broadcaster  ──── lastFrame (snapshot)
      │  fan-out channels
      ▼
HTTP clients  ←── MJPEG multipart  (image/*)
              ←── raw H.264 stream (video/h264)
```

---

## Adding a New Capture Backend

1. Create `src/modules/video/<name>/<name>.go` implementing `server.VideoSource`.
2. Add a `Capture<Name> CaptureType` constant in `src/server/source.go`.
3. Add a `case` in `SourceCache.Get` in `src/server/source.go`.
4. Update `docs/api.md` — add the new `?type=<name>` value to the table.
