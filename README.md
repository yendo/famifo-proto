# famifo-proto

Indexes photos on a local disk and serves them as a browsable gallery to any browser on your LAN.

## Features

- Indexes photos and presents them as a single gallery, ignoring the folder hierarchy
- Ordered by EXIF capture time, falling back to the file's modification time
- Full scan at startup, then follows changes automatically via fsnotify
- A single binary. No cgo, no external database server

## Supported formats

| Extension | Thumbnail | Notes |
|---|---|---|
| `.jpg` `.jpeg` `.png` `.gif` `.webp` | generated | |
| `.heic` `.heif` | not generated | The original is served as-is, so it will not display outside Safari |

Video is out of scope.

## Build

```bash
CGO_ENABLED=0 go build -o famifo-proto .
```

## Usage

```bash
./famifo-proto -dir /path/to/photos
```

| Flag | Default | Description |
|---|---|---|
| `-dir` | (required) | Directory to collect photos from |
| `-data` | `./famifo-data` | Where the database and thumbnail cache are stored |
| `-addr` | `:8080` | HTTP listen address |
| `-thumb` | `480` | Thumbnail size, longest edge in pixels |

## Using the gallery

- Photos are grouped by capture date. A day that fits on one row sits alongside its neighbours
- The scrollbar spans the whole date range, so any position is one drag away
- Dragging the scrubber at the right edge moves through the library with the year and month shown
- Tap a tile to enlarge it. Swipe left/right to move between photos, swipe down to close

## Browser tests

There are tests that exercise `internal/web/static/app.js` (virtual scrolling, the per-day
layout calculation, the lightbox and the date scrubber) by actually running it in headless
Chrome inside a Docker container. They carry `//go:build browser`, so a plain `go test ./...`
does not include them.

Pull the image first. The host's Chrome is never used — the tests connect to the container's
Chrome with `chromedp.NewRemoteAllocator`.

```bash
docker pull chromedp/headless-shell:latest
```

Run them with:

```bash
CGO_ENABLED=0 go test -tags browser ./internal/web/ -v
```

`TestMain` starts the container, waits for it, and cleans it up. Where Docker or the image is
unavailable, each test skips itself individually through `requireBrowser` — the non-browser
tests in the same package still run as usual.

In CI, or anywhere else you would rather not let a missing environment pass unnoticed, set
`FAMIFO_BROWSER_TESTS=required`. Missing Docker or a missing image then fails instead of
skipping.

## Limitations

- **Local disks only.** fsnotify cannot receive change notifications from network file systems
  (NFS/SMB), so the target directory has to be mounted locally. Pointing it straight at a NAS
  share will not work — the intended setup is to run it on a machine on the LAN and let it serve
  that machine's local disk.
- **Meant for use inside a LAN.** Neither authentication nor HTTPS is implemented. To reach it
  from outside, connect to your home LAN over a VPN (Tailscale or similar) rather than opening a
  port.
- **A scan that finds zero photos does not delete anything.** Starting up while an external drive
  is unmounted produces an empty scan, which looks exactly like "everything was deleted". To keep
  that from wiping the index, `FullScan` skips deletions when the scan is empty *and* the existing
  index is not, and logs a `走査結果が空のため削除をスキップした` warning. The side effect is that
  if you really did delete every photo, the database rows and thumbnails stay behind and the same
  warning appears on every startup. To recover, delete the data directory (`-data`, default
  `./famifo-data`) and start again — the database and thumbnail cache are rebuilt.

## Design

See [docs/design.md](docs/design.md) for the design decisions and the reasoning behind them.
