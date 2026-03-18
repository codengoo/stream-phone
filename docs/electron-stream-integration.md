# Electron Stream Integration Guide

This document describes how to start the Go screen-stream server and consume it
from an Electron renderer process.

---

## Architecture overview

```
Android device
   │  (USB / TCP)
   └─► ADB  ──►  minicap (on-device binary)
                    │  Unix domain socket  ──►  adb forward
                                                    │  tcp:1717
                                                    └─► Go stream.Server
                                                              │  HTTP :9373
                                                              └─► Electron renderer
```

1. **`adb.SystemManager`** manages one device (serial stored once in the struct).
2. **`minicap.Manager`** sets up and drives minicap for that device.
3. **`stream.Server`** wraps a `minicap.Manager`, exposes JPEG frames over HTTP,
   and fans them out to every connected client.

---

## Go: starting the server

```go
package main

import (
    "context"
    "log"
    "path/filepath"

    "automation/src/modules/adb"
    "automation/src/modules/video/minicap"
    "automation/src/modules/video/stream"
)

func main() {
    ctx := context.Background()

    adbManager := adb.NewManager(filepath.Join(".", "bin"))

    // One Manager per device — serial is fixed at construction time.
    mc := minicap.New(adbManager, "emulator-5554", filepath.Join(".", "bin", "minicap-cache"))

    srv := stream.New(mc, ":9373")
    if err := srv.Serve(ctx); err != nil {
        log.Fatalf("stream server: %v", err)
    }
}
```

Run the server before (or alongside) your Electron app. The server auto-downloads
`adb` and the minicap binaries on first use.

---

## HTTP endpoints

| Method | Path        | Description                                             |
|--------|-------------|---------------------------------------------------------|
| `GET`  | `/stream`   | Infinite MJPEG multipart stream (use as `<img src>`)   |
| `GET`  | `/snapshot` | Single JPEG — the latest captured frame                 |
| `GET`  | `/info`     | JSON with `width`, `height`, `density`                  |
| `GET`  | `/health`   | Plain-text `"ok"` liveness probe                        |

Default address: `http://localhost:9373`

---

## Electron renderer integration

### Option A — live video (recommended, zero JS)

Set an `<img>` source to the `/stream` endpoint. The browser's built-in MJPEG
decoder renders each frame as it arrives.

```html
<!-- renderer/index.html -->
<img id="device-screen"
     src="http://localhost:9373/stream"
     alt="device screen"
     style="max-width: 100%;" />
```

That's it. The browser handles decoding; no JavaScript is required.

> **Electron security note**: if your `BrowserWindow` has
> `webSecurity: true` (the default), requests to `localhost` from a
> `file://` page are blocked by CORS. Use one of these fixes:
>
> ```js
> // main.js — BrowserWindow options
> webPreferences: {
>     webSecurity: false,  // dev only, or …
> }
>
> // … or set a proper Content-Security-Policy in your HTML:
> // <meta http-equiv="Content-Security-Policy"
> //       content="img-src http://localhost:9373">
> ```

---

### Option B — snapshot polling

Fetch a single frame on demand (e.g. for screenshots or low-frequency updates).

```js
// renderer.js
async function captureSnapshot() {
    const res = await fetch('http://localhost:9373/snapshot');
    if (!res.ok) throw new Error(`snapshot failed: ${res.status}`);
    const blob = await res.blob();
    const url  = URL.createObjectURL(blob);
    document.getElementById('device-screen').src = url;
}

// Refresh every 500 ms
setInterval(captureSnapshot, 500);
```

---

### Option C — stream via fetch (ReadableStream)

For frame-by-frame control with frame-ready callbacks, parse the MJPEG
multipart stream manually.

```js
// renderer.js
async function startStream(onFrame) {
    const res = await fetch('http://localhost:9373/stream');
    const reader = res.body.getReader();
    const SOI = new Uint8Array([0xff, 0xd8]); // JPEG start-of-image marker
    const EOI = new Uint8Array([0xff, 0xd9]); // JPEG end-of-image marker

    let buf = new Uint8Array(0);

    while (true) {
        const { value, done } = await reader.read();
        if (done) break;

        // Append incoming bytes to accumulator
        const tmp = new Uint8Array(buf.length + value.length);
        tmp.set(buf);
        tmp.set(value, buf.length);
        buf = tmp;

        // Extract complete JPEG frames
        let start = indexOfSeq(buf, SOI);
        while (start !== -1) {
            const end = indexOfSeq(buf, EOI, start + 2);
            if (end === -1) break;
            const frameEnd = end + 2;
            const frame = buf.slice(start, frameEnd);
            onFrame(frame);
            buf = buf.slice(frameEnd);
            start = indexOfSeq(buf, SOI);
        }
    }
}

function indexOfSeq(haystack, needle, from = 0) {
    outer: for (let i = from; i <= haystack.length - needle.length; i++) {
        for (let j = 0; j < needle.length; j++) {
            if (haystack[i + j] !== needle[j]) continue outer;
        }
        return i;
    }
    return -1;
}

// Usage:
startStream(frame => {
    const blob = new Blob([frame], { type: 'image/jpeg' });
    const url  = URL.createObjectURL(blob);
    const prev = document.getElementById('device-screen').src;
    document.getElementById('device-screen').src = url;
    if (prev.startsWith('blob:')) URL.revokeObjectURL(prev);
});
```

---

## Device info

Retrieve screen dimensions and DPI before rendering:

```js
const res  = await fetch('http://localhost:9373/info');
const info = await res.json();
// info = { "Width": 1080, "Height": 1920, "Density": 420 }

console.log(`${info.Width}×${info.Height} @ ${info.Density} dpi`);
```

Use `Width` / `Height` to size the `<img>` element correctly:

```js
const img = document.getElementById('device-screen');
img.style.width  = `${info.Width / (info.Density / 96)}px`;
img.style.height = `${info.Height / (info.Density / 96)}px`;
```

---

## Liveness & error handling

Poll `/health` before showing the stream to give the user feedback while
minicap is initialising (can take a few seconds on first run):

```js
async function waitForServer(maxMs = 30_000) {
    const deadline = Date.now() + maxMs;
    while (Date.now() < deadline) {
        try {
            const res = await fetch('http://localhost:9373/health');
            if (res.ok) return;
        } catch { /* not up yet */ }
        await new Promise(r => setTimeout(r, 500));
    }
    throw new Error('stream server did not start in time');
}

await waitForServer();
document.getElementById('device-screen').src = 'http://localhost:9373/stream';
```

---

## Spawning the Go server from Electron main process

```js
// main.js (Electron main process)
const { spawn } = require('child_process');
const path = require('path');

const serverBin = path.join(__dirname, 'bin', 'automation-server');

const server = spawn(serverBin, [], {
    stdio: ['ignore', 'pipe', 'pipe'],
});

server.stdout.on('data', d => console.log('[server]', d.toString()));
server.stderr.on('data', d => console.error('[server]', d.toString()));

app.on('before-quit', () => server.kill());
```

Build the Go binary before packaging:

```sh
go build -o bin/automation-server ./src
```
