# famifo-proto

Indexes photos and videos on a local disk and serves them as a browsable gallery to any browser on your LAN.

## Features

- Indexes photos and videos and presents them as a single gallery, ignoring the folder hierarchy
- Ordered by capture time, read from EXIF for photos and from the container for videos, falling back to the file's modification time
- Full scan at startup, then follows changes automatically via fsnotify
- A single binary. No cgo, no external database server

## Supported formats

| Extension | Thumbnail | Notes |
|---|---|---|
| `.jpg` `.jpeg` `.png` `.gif` `.webp` | generated | |
| `.heic` `.heif` | borrowed from Synology if one is there | Synology's large JPEG is served in place of the original, so these display outside Safari too. With nothing to borrow the original is served as-is and only Safari shows it |
| `.mp4` `.mov` | borrowed from Synology if one is there | famifo never decodes video, so it makes no thumbnail of its own. With nothing to borrow the tile carries a play mark and no picture |

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

Videos borrow twice. The tile comes from `SYNOPHOTO_THUMB_M.jpg` just like a photo's, and
playing one serves `SYNOPHOTO_FILM_H.mp4` — Synology's H.264 transcode — instead of the
original. Phones record HEVC, which plays only where the device has a hardware decoder, so
the transcode is what makes a video watchable on Android and on a PC. Unlike the still
thumbnails, a transcode is not implied by the thumbnail being there: the two are separate
jobs and either can fail on its own, so its presence is checked directly. A video with no
transcode to borrow gets its original, and whether it plays is up to the device.

`@eaDir` is only ever read. famifo never writes to or deletes anything inside it.

## Build

```bash
CGO_ENABLED=0 go build -o famifo-proto .
```

## Releases

Tagged builds are published to [GitHub Releases](https://github.com/yendo/famifo-proto/releases)
as `linux/amd64` and `linux/arm64` tarballs, and to `ghcr.io/yendo/famifo-proto` under both
the tag and `latest`. No tarball is built for untagged commits, but every push to `main`
publishes an image tagged `latest` and `sha-<commit>`.

```bash
git tag -a v0.1.0 -m "v0.1.0"
git push origin v0.1.0
```

Check `git status` first — goreleaser refuses to release from a dirty tree, and a stray
untracked file would otherwise stamp the binaries `+dirty`.

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
| `-scan-workers` | half the CPUs | How many photos are taken in at once, both by the scan and by the watcher |
| `-version` | | Print the build version and exit |

The version comes from the build itself: a tagged build reports the tag, any
other build reports a pseudo-version carrying the commit, and uncommitted
changes add `+dirty`.

### Timezone

Photos are grouped by the day they were taken, which depends on the machine's local timezone.
The binary embeds the IANA database, so setting `TZ` is enough even where the filesystem has no
zoneinfo — a bare container, for instance. Without it the process falls back to UTC and files
those photos under the wrong day. The startup log prints the zone it resolved:

```
msg=starting version="v0.1.0" timezone=JST+09:00 dirs=[/photos] ...
```

Check that line before letting a first index run to completion; rebuilding one costs hours.

## Authentication

Off by default: without `-oidc-issuer`, anyone who can reach the address sees the
photos. Pass it and famifo hands the visitor to an OpenID Connect provider and shows
nothing until they come back identified. Everyone the provider accepts gets in —
famifo keeps no user list of its own, so who may look is decided where the accounts
already live.

```bash
docker run -d --restart unless-stopped -p 8080:8080 \
  -v /volume1/photo:/photos:ro \
  -v /volume1/famifo/data:/data \
  -e FAMIFO_OIDC_CLIENT_SECRET=<secret> \
  famifo -dir /photos -data /data \
  -oidc-issuer https://nas.example.com:5001/webman/sso \
  -oidc-client-id <client id> \
  -external-url https://nas.example.com:8443
```

| Setting | Meaning |
|---|---|
| `-oidc-issuer` | The provider's issuer, **including the port**. Empty turns authentication off |
| `-oidc-client-id` | The client id registered with the provider |
| `-external-url` | The URL famifo is reached at from outside. The redirect URI is this plus `/auth/callback` |
| `-oidc-logout-url` | Optional. The provider's own logout URL. Set only alongside `-oidc-issuer`. See [Sessions](#sessions) |
| `FAMIFO_OIDC_CLIENT_SECRET` | The client secret. Read from the environment, never from a flag — command-line arguments are readable by anyone on the host through `/proc` |

famifo never sees a password. Two-factor authentication, lockouts and password
changes all stay with the provider.

### With Synology SSO Server

Install the SSO Server package, enable OIDC, and set its server URL to the NAS with
**the DSM port included**, for example `https://nas.example.com:5001`. Leaving the port
out looks like it works — the discovery document is served — but every login then fails
at the token exchange: without a port the advertised endpoints land on 443, which serves
DSM's static default site and answers POST with `405 Not Allowed`.

Register famifo as an application and set its redirect URI to `-external-url` plus
`/auth/callback`, character for character. A mismatch is rejected by the provider.

DSM's certificate is easiest to get from the DDNS screen: registering a Synology DDNS
hostname with the certificate box ticked obtains a Let's Encrypt certificate through
Synology's own DNS, so no port has to be opened. Taking one from the certificate screen
uses HTTP-01 instead and needs port 80 reachable from the internet.

### Serving famifo over HTTPS

famifo speaks plain HTTP and does not terminate TLS. Put it behind DSM's reverse proxy
(Control Panel → Login Portal) and let DSM present the certificate it already has:

```
https://nas.example.com:8443  ->  http://localhost:8080
```

When `-external-url` starts with `https://`, famifo marks its cookies `Secure`. It never
infers this from a header.

### Resolving the provider's name inside the LAN

The browser and famifo both reach the provider by name, and that name normally points at
your public address. With no ports open, nothing on the LAN can reach it there, so the
name has to resolve to the NAS locally.

1. Install the **DNS Server** package on the NAS. Under Resolution, enable resolution and
   set a forwarder, or everything except your own zone stops resolving.
2. Create a master zone for the DDNS name with an A record pointing at the NAS.
3. On the router, forward that domain to the NAS. On an NTT home gateway this is under
   Local Domain. **That field only accepts IPv6 addresses** — an IPv4 address is rejected
   as out of range, which reads like a typo but is not.

If the container cannot resolve the name at startup it refuses to start and logs
`cannot read the OIDC discovery document`. Pin the address if that happens:

```bash
docker run --add-host nas.example.com:192.168.1.2 ...
```

**If logins break one day and nothing else does, look here first.** The address written
into the router is the NAS's IPv6 address, and while its host half is derived from the
MAC and never moves, the prefix is handed out by the ISP. It survives reboots and power
cuts but not a replaced or reset gateway, a re-provisioned line, or a change of provider.
The symptom is narrow and misleading: famifo refuses every login while the rest of the
internet works. Compare the address in the router's Local Domain settings with the NAS's
current IPv6 address.

### Sessions

After a successful login famifo issues its own signed cookie and stops asking the
provider. Sessions last 30 days and survive restarts, because the signing key lives in
`<data>/session.key`.

Individual sessions cannot be revoked, and deleting `session.key` and restarting is not
a substitute: it invalidates every famifo session, but any device whose provider session
is still alive is signed straight back in on its next visit without being asked for
anything, confirmed on hardware by a fresh sign-in appearing in the log seconds after
the restart. Real revocation lives at the provider — disable the account, or end its
sessions there.

Signing out of famifo does not sign you out of the provider by itself: the provider
offers no logout endpoint, so the same browser can normally walk straight back in. Set
`-oidc-logout-url` to change that: `/logout` then clears famifo's cookies and sends the
browser to that URL, so one press of the button ends both sessions. Leave it unset and
the button only ends famifo's own session, as above. For Synology SSO Server the value
is `https://<host>:5001/webman/logout.cgi`.

## Docker

The image is built `FROM scratch` around the static binary — 15MB, no runtime
dependencies.

```bash
docker build -t famifo .
```

`.git` is part of the build context, so `go build` stamps the version by itself —
nothing has to be passed in.

### Running it

```bash
docker run -d --restart unless-stopped -p 8080:8080 \
  -v /volume1/photo:/photos:ro \
  -v /volume1/famifo/data:/data \
  famifo
```

Photo directories are mounted read-only. `:ro` is enforced by the kernel, so even a
root process inside the container cannot delete them.

To index several separate locations, mount each one and name it as a root:

```bash
docker run -d --restart unless-stopped -p 8080:8080 \
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

The container runs as uid/gid `65534:65534` — `nobody:nogroup`, the conventional
unprivileged id, tied to no particular host. Docker cannot address a user by name here —
a `scratch` image has no `/etc/passwd` — so the id is numeric. The data directory has to
be owned by it:

```bash
sudo chown 65534:65534 /volume1/famifo/data
```

To run as some other account instead, pass it at run time; no rebuild is needed:

```bash
id famifo                                  # find the uid and gid
sudo chown 1000:1000 /volume1/famifo/data  # let it write there
docker run --user 1000:1000 ...
```

Do not pick an id of 65536 or above. Under userns-remap and rootless Docker the subuid
allocation is 65536 wide by default — container uids `0..65535` — and anything past it
cannot be mapped, so the container fails to start.

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
- **Meant for use inside a LAN.** HTTPS is not implemented; authentication is optional and off by default.
  To reach it from outside, connect to your home LAN over a VPN (Tailscale or similar) rather than
  opening a port.
- **A root that scans empty loses nothing.** Starting up while an external drive is unmounted
  produces an empty scan of that root, which looks exactly like "everything under it was
  deleted". `Scan` therefore judges each root separately: a root that turns up no photos
  keeps its existing entries, even when the other roots are healthy, and logs a
  `skipped deletions because a root scanned empty` warning. A root it cannot read at all is
  skipped the same way, with `skipped an unreadable root`, rather than aborting the scan and
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
