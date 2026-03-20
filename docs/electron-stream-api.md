# Stream Server API — Electron Integration Guide

> **For:** Electron / frontend developers  
> **Server:** Go HTTP server running on the host machine (default port **9373**)  
> **Base URL:** `http://localhost:9373` (configurable at startup)

---

## Quick Start

1. The Go server exposes a streaming endpoint the moment a source is activated.
2. Before rendering the screen, the Electron main process should:
   - Call `GET /health` to confirm the server is alive.
   - Call `POST /admin/source` to activate a capture source (`"minicap"` or `"adbcap"`).
   - Call `GET /info` to learn screen dimensions and decide display layout.
3. The renderer can then attach `GET /stream` to an `<img>` or `<video>` element.

---

## Public Endpoints

### `GET /health`
Liveness probe.

```
Response 200 text/plain
ok
```

---

### `GET /info`
Returns device screen metrics.

```jsonc
// Response 200 application/json
{
  "Width": 1080,
  "Height": 2400,
  "Orientation": 0,          // 0=portrait, 1=landscape-left, 3=landscape-right
  "Rotation": 0,             // degrees (Orientation * 90)
  "Density": {
    "Physical": 420.0,
    "Override": 420.0,
    "Current":  420.0,
    "Scale":    1.0
  }
}
```

Error (no active source):
```
Response 503 — "no active source — call POST /admin/source first"
```

---

### `GET /stream`
Infinite stream of screen frames.

| Source | Content-Type | Recommended consumer |
|--------|-------------|----------------------|
| `minicap` | `multipart/x-mixed-replace; boundary=mjpegframe` | `<img src="...">` |
| `adbcap`  | `video/h264` (raw Annex-B H.264) | `<video>` + MediaSource |

**MJPEG — simplest, works out of the box:**
```html
<img id="screen" src="http://localhost:9373/stream" />
```

**Raw H.264 — lower bandwidth, needs a JS demuxer:**
```js
// Requires a transmuxer library (e.g. mux.js, mp4box.js) that converts
// raw Annex-B H.264 into fragmented MP4 (fMP4) for MediaSource.
const video = document.querySelector('video');
const ms    = new MediaSource();
video.src   = URL.createObjectURL(ms);

ms.addEventListener('sourceopen', async () => {
  const mime = 'video/mp4; codecs="avc1.42E01E"';
  if (!MediaSource.isTypeSupported(mime)) {
    console.error('codec not supported');
    return;
  }
  const sb = ms.addSourceBuffer(mime);

  const res    = await fetch('http://localhost:9373/stream');
  const reader = res.body.getReader();

  // Simple queue to avoid appending while buffer is updating
  const queue = [];
  sb.addEventListener('updateend', () => {
    if (queue.length > 0) sb.appendBuffer(queue.shift());
  });

  while (true) {
    const { value, done } = await reader.read();
    if (done) break;
    // value is raw H.264 — pass it through your transmuxer first,
    // then push fMP4 segments into the queue.
    const fmp4Segment = transmux(value); // your transmuxer call
    if (sb.updating || queue.length > 0) {
      queue.push(fmp4Segment);
    } else {
      sb.appendBuffer(fmp4Segment);
    }
  }
});
```

> **Tip:** For MVP, start with MJPEG (`minicap` source + `<img>`). Switch to H.264 + MediaSource when you need lower bandwidth or frame-accurate seeking.

Error (no active source):
```
Response 503 — "no active source — call POST /admin/source first"
```

---

### `GET /snapshot`
Returns a single image frame (the most recently received one).

```
Response 200  Content-Type: image/jpeg  (or image/* depending on source)
<binary image data>
```

```js
async function takeSnapshot() {
  const res  = await fetch('http://localhost:9373/snapshot');
  const blob = await res.blob();
  return URL.createObjectURL(blob);  // use as <img src="...">
}
```

Error (no frame captured yet):
```
Response 503 — "no frame available yet — stream may still be starting"
```

---

## Admin Endpoints

These are **called from the Electron main process**, not from the renderer.  
All responses are JSON. All endpoints set `Access-Control-Allow-Origin: *`.

---

### `GET /admin/sources`
List all registered capture sources.

```jsonc
// Response 200
{
  "sources": [
    { "name": "minicap", "contentType": "image/jpeg",  "active": true  },
    { "name": "adbcap",  "contentType": "video/h264",  "active": false }
  ]
}
```

---

### `POST /admin/source`
Switch the active capture source.  
The server performs a pre-check (ADB device reachability, `ScreenInfo` call) before starting the new source.

**Request:**
```json
{ "source": "minicap" }
```

| Status | Meaning |
|--------|---------|
| `204 No Content` | Source switched successfully, streaming will resume momentarily. |
| `400 Bad Request` | Missing or malformed body. |
| `503 Service Unavailable` | Unknown source name, or pre-check failed (device not accessible). |

```js
// Electron main process example
async function switchSource(name) {
  const res = await fetch('http://localhost:9373/admin/source', {
    method:  'POST',
    headers: { 'Content-Type': 'application/json' },
    body:    JSON.stringify({ source: name }),
  });
  if (!res.ok) {
    const err = await res.json();
    throw new Error(err.error);
  }
}
```

---

### `GET /admin/status`
Full service health and metrics snapshot.

```jsonc
// Response 200
{
  "current":    "minicap",
  "sources": [
    { "name": "minicap", "contentType": "image/jpeg", "active": true },
    { "name": "adbcap",  "contentType": "video/h264", "active": false }
  ],
  "frameCount": 1234,        // total frames delivered since startup
  "byteCount":  56789012,    // total bytes delivered since startup
  "screenInfo": {            // null if source not yet active
    "Width": 1080,
    "Height": 2400,
    "Orientation": 0,
    "Rotation": 0,
    "Density": { "Physical": 420, "Override": 420, "Current": 420, "Scale": 1 }
  },
  "time": "2026-03-20T10:00:00Z"
}
```

---

## Recommended Electron Startup Flow

```js
// In the Electron main process (IPC or preload bridge)

const BASE = 'http://localhost:9373';

async function initStream(sourceName = 'minicap') {
  // 1. Wait for the Go server to be ready
  await waitForHealth();

  // 2. Activate the desired source (performs ADB pre-checks on the Go side)
  await fetch(`${BASE}/admin/source`, {
    method:  'POST',
    headers: { 'Content-Type': 'application/json' },
    body:    JSON.stringify({ source: sourceName }),
  }).then(r => { if (!r.ok) throw new Error(`switch source failed: ${r.status}`); });

  // 3. Fetch screen info for layout
  const info = await fetch(`${BASE}/info`).then(r => r.json());

  // 4. Tell the renderer to attach the stream
  mainWindow.webContents.send('stream-ready', { info, base: BASE });
}

async function waitForHealth(retries = 20, delayMs = 500) {
  for (let i = 0; i < retries; i++) {
    try {
      const res = await fetch(`${BASE}/health`);
      if (res.ok) return;
    } catch (_) { /* server not up yet */ }
    await new Promise(r => setTimeout(r, delayMs));
  }
  throw new Error('stream server did not become healthy in time');
}
```

```js
// In the renderer (preload bridge exposes ipcRenderer)
ipcRenderer.on('stream-ready', (_, { info, base }) => {
  // Resize canvas / layout to match screen dimensions
  applyScreenLayout(info.Width, info.Height, info.Rotation);

  // Attach stream
  document.getElementById('screen').src = `${base}/stream`;
});
```

---

## CORS

All endpoints set `Access-Control-Allow-Origin: *`.  
This is intentional for Electron development (renderer runs on `file://` or `app://`).

---

## Error Handling Cheatsheet

| Endpoint | 503 reason | Action |
|----------|-----------|--------|
| `GET /stream`    | No active source | Call `POST /admin/source` first |
| `GET /snapshot`  | No frames yet   | Retry after a short delay |
| `GET /info`      | No active source | Call `POST /admin/source` first |
| `POST /admin/source` | Device not accessible | Check ADB connection, retry |
