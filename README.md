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
| `.heic` `.heif` | borrowed from Synology if one is there | Synology's large JPEG is served in place of the original, so these display outside Safari too. With nothing to borrow the original is served as-is and only Safari shows it |

Video is out of scope.

### Synology thumbnails

On a Synology NAS the thumbnails Synology Photos already made are served directly from
`<photo directory>/@eaDir/<photo name>/SYNOPHOTO_THUMB_M.jpg` instead of generating new ones.
Nothing is copied, decoding is skipped entirely, and HEIC photos get a thumbnail that famifo
cannot produce on its own.

Enlarging a HEIC serves `SYNOPHOTO_THUMB_XL.jpg` (1707px on the long edge) from that same
directory instead of the original. famifo cannot decode HEIC and no browser but Safari will
display it, so the borrowed JPEG is what makes those photos viewable on Android and on a PC.
Safari gives up some resolution in exchange. A HEIC with nothing to borrow still gets its
original.

`@eaDir` is only ever read. famifo never writes to or deletes anything inside it.

## Build

```bash
CGO_ENABLED=0 go build -o famifo-proto .
```

## Usage

```bash
./famifo-proto -dir /path/to/photos
```

Several roots can be given at once, separated by the OS list separator (`:` on Unix):

```bash
./famifo-proto -dir /home/alice/Photos:/home/bob/Photos
```

Roots may not be duplicated or nested inside one another, and `-data` has to sit outside
every one of them.

| Flag | Default | Description |
|---|---|---|
| `-dir` | (required) | Directories to collect photos from, `:`-separated |
| `-data` | `./famifo-data` | Where the database and generated thumbnails are stored |
| `-addr` | `:8080` | HTTP listen address |
| `-version` | | Print the build version and exit |

### Timezone

Photos are grouped by the day they were taken, which depends on the machine's local timezone.
The binary embeds the IANA database, so setting `TZ` is enough even where the filesystem has no
zoneinfo — a bare container, for instance. Without it the process falls back to UTC and files
those photos under the wrong day. The startup log prints the zone it resolved:

```
msg=起動 version="a4272a5b (2026-08-26T14:27:20Z)" timezone=JST+09:00 dirs=[/photos] ...
```

Check that line before letting a first index run to completion; rebuilding one costs hours.

## Docker

The image is built `FROM scratch` around the static binary — 15MB, no runtime
dependencies.

```bash
docker build --build-arg VERSION=$(git rev-parse --short HEAD) -t famifo .
```

`.git` is kept out of the build context, so Go's automatic VCS stamping has nothing to
read and `-version` would report `dev`. Pass `VERSION` and it is embedded instead.

### Running it

```bash
docker run -d --restart unless-stopped -p 8080:8080 \
  --user 1029:100 \
  -v /volume1/photo:/photos:ro \
  -v /volume1/famifo/data:/data \
  famifo
```

Photo directories are mounted read-only. `:ro` is enforced by the kernel, so even a
root process inside the container cannot delete them.

To index several separate locations, mount each one and name it as a root:

```bash
docker run -d --restart unless-stopped -p 8080:8080 \
  --user 1029:100 \
  -v /volume1/photo:/photos/main:ro \
  -v /mnt/usb:/photos/usb:ro \
  -v /volume1/famifo/data:/data \
  famifo -dir /photos/main:/photos/usb -data /data
```

Split them by what can disappear independently. The per-root guard described under
Limitations only helps when a root is its own root: with the default single `-dir
/photos`, one mount going missing does not make `/photos` look empty, so nothing stops
its photos being dropped from the index. Splitting a single mount into several roots
buys nothing, since they come and go together.

### The `/data` mount is not optional

Left out, the database and generated thumbnails land in the container's writable
layer and are gone the moment the container is recreated — which is exactly what
updating the image on DSM does. That costs a full reindex, and nothing reveals it
until the first update.

### Running as a non-root user

The container defaults to uid/gid `1029:100`. Docker cannot address a user by name here —
a `scratch` image has no `/etc/passwd` — so the id is numeric. To match an account on
the host:

```bash
ssh nas 'id famifo'                                     # find the uid and gid
ssh nas 'sudo chown 1029:100 /volume1/famifo/data'      # let it write there
docker run --user 1029:100 ...                          # no rebuild needed
```

`--build-arg UID=1029 --build-arg GID=100` bakes the same thing into the image, for
environments whose UI cannot pass `--user`.

Ownership of a bind mount comes from the host directory and is not adjusted by Docker,
so the directory has to be writable by that id beforehand. Named volumes behave
differently — they inherit ownership from the image — but a bind mount keeps the data
directory visible on the NAS, where deleting it to force a rebuild is a file-manager
operation rather than a shell one. Losing it costs only rebuild time; it holds nothing
that is not derived from the photos.

### Timezone in a container

`TZ` is set to `Asia/Tokyo` in the image. It matters: see the Timezone section above.
Check the startup log after any change to the run command.

```bash
docker logs <container> | head -1
```


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
- **A root that scans empty loses nothing.** Starting up while an external drive is unmounted
  produces an empty scan of that root, which looks exactly like "everything under it was
  deleted". `FullScan` therefore judges each root separately: a root that turns up no photos
  keeps its existing entries, even when the other roots are healthy, and logs a
  `走査結果が空のルートがあるため削除をスキップした` warning. A root it cannot read at all is
  skipped the same way, with `ルートを読めないため飛ばした`, rather than aborting the scan and
  stalling the healthy roots. The side effect is that if you really did empty a root, its rows
  and thumbnails stay behind and the warning repeats on every startup. To recover, delete the
  data directory (`-data`, default `./famifo-data`) and start again — the database and the
  thumbnails are rebuilt.
- **Dropping a root from `-dir` deletes its photos from the index.** The index follows what you
  currently point it at. Photos under a path that is no longer a root are removed, thumbnails
  included, and getting them back means reindexing. This is the one case the guard above does
  not cover, because the root is absent rather than empty.

## Design

See [docs/design.md](docs/design.md) for the design decisions and the reasoning behind them.
