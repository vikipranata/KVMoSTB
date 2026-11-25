<p align="center">
  <img src="cmd/kvmostb/web/kvmostb.png" alt="KVMoSTB logo" width="160">
</p>

<h1 align="center">KVMoSTB</h1>

<p align="center">
  A cheap and lightweight KVM over IP for homelab PC from a set-top box.
</p>

When a homelab PC will not boot, or needs a reinstall, you end up carrying a monitor and a keyboard to the rack. KVMoSTB replaces that trip with a browser tab. KVM over IP means you see the machine's screen and drive its keyboard and mouse across the network. KVMoSTB adds a virtual CD drive, so the machine can boot from an ISO that sits on your network.

It runs on an HG680-FJ set-top box with [Armbian](https://www.armbian.com), a Debian-based Linux for ARM boards. You need no dedicated KVM hardware: the STB, a USB capture dongle, a USB cable and an HDMI cable are the whole kit. The dongle reads the target's HDMI output, and the STB's USB OTG port presents a keyboard, a mouse and a CD-ROM drive to the target. The target sees ordinary USB devices and has nothing to install. The aim is for the same path to work in BIOS and UEFI, but firmware has not been tested yet (see [Limitations](#limitations)).

The software is one [Go](https://go.dev) binary of about 8 MB with the web console inside it. On the STB it used about 19 MB of memory while serving the console, out of the board's 2 GB. It reads video through [V4L2](https://www.kernel.org/doc/html/latest/userspace-api/media/v4l/v4l2.html) as MJPEG, builds the USB gadget through [Linux configfs](https://www.kernel.org/doc/html/latest/usb/gadget_configfs.html), and serves a framework-free HTML and JavaScript console over HTTP and WebSocket. systemd starts it at boot, and a `.deb` package installs it.

| Layer | Technology |
| --- | --- |
| Board | HG680-FJ, Amlogic S905X2, arm64 |
| Operating system | Armbian (Debian Trixie), Linux 6.12 |
| Language | Go 1.24, with cgo for V4L2 |
| Video | MS2109 USB capture dongle, V4L2, MJPEG |
| Keyboard and mouse | dwc2 USB OTG gadget, configfs, `f_hid` |
| Virtual media | `f_mass_storage`, ISO files from NFS or a local folder |
| Console | Vanilla HTML and JavaScript, HTTP and WebSocket, form login with a session cookie |
| Packaging | `.deb`, systemd, GitHub Actions release workflow |

## Contents

- [Features](#features)
- [Architecture](#architecture)
- [Hardware](#hardware)
- [Screenshots](#screenshots)
- [How it works](#how-it-works)
- [Build](#build)
- [Install](#install)
- [Configuration](#configuration)
- [Limitations](#limitations)
- [Credits](#credits)
- [License](#license)

## Features

- **Video.** MJPEG from a USB capture dongle at 640x480, 1280x720 or 1920x1080, at 25, 30 or 60 fps. The default is 1280x720 at 30 fps. Fullscreen, screenshots, and live resolution, frame rate and bitrate.
- **Keyboard and mouse.** Physical keys, a full-size on-screen keyboard with sticky Ctrl and Alt, a clipboard box that types text into the target, and an absolute mouse.
- **Virtual media.** Mount an ISO from an NFS share or a local folder as a USB CD-ROM, and unmount it, from the console.
- **Sign-in.** A form login with a session cookie. It fails closed: with no credentials configured, nobody can sign in.
- **Boot to ready.** Two systemd units bring the USB gadget and the console up after every reboot.

## Architecture

```mermaid
flowchart LR
    subgraph Target["Target PC"]
        T_HDMI["HDMI out"]
        T_USB["USB port"]
    end

    subgraph STB["HG680-FJ running kvmostb"]
        CAP["capture<br/>V4L2 MJPEG"]
        GAD["gadget<br/>configfs HID + mass storage"]
        HID["hid<br/>keyboard and mouse reports"]
        ISO["iso<br/>image list"]
        API["HTTP server<br/>session auth, JSON API, WebSocket"]
    end

    DONGLE["MS2109 USB capture dongle"]
    NFS[("ISO folder<br/>NFS or local")]
    BROWSER["Browser<br/>web console"]

    T_HDMI -->|HDMI| DONGLE
    DONGLE -->|USB host port| CAP
    CAP -->|MJPEG stream| API
    API <-->|HTTP and /ws/hid| BROWSER
    API -->|input frames| HID
    HID -->|reports| GAD
    GAD -->|OTG port: keyboard, mouse, CD-ROM| T_USB
    NFS --> ISO
    ISO --> API
    API -->|mount or unmount| GAD
```

| Package | Role |
| --- | --- |
| `cmd/kvmostb` | Entry point, HTTP routing, session auth and the embedded web console. |
| `internal/capture` | Opens the V4L2 device, builds the resolution menu and streams MJPEG. |
| `internal/gadget` | Builds the USB gadget in configfs, binds the UDC and swaps the ISO. Its async writer ships reports to `/dev/hidg0` and `/dev/hidg1`. |
| `internal/hid` | Turns browser key codes and mouse events into boot-keyboard and absolute-mouse reports. |
| `internal/iso` | Lists `.iso` files in the search roots. |
| `internal/logging` | Leveled logging on top of the standard library. |

## Hardware

### What you need

| Part | Notes |
| --- | --- |
| STB HG680-FJ | Amlogic S905X2, 4x Cortex-A53, 2 GB RAM, 8 GB eMMC. Runs Armbian Trixie with a 6.12 kernel (`6.12.117-ophub` was tested). |
| MACROSILICON MS2109 capture dongle (`534d:2109`) | HDMI in, UVC out. Plug it into the STB's USB 3.0 host port. |
| USB A-to-A cable | From the STB's USB 2.0 port to a USB port on the target PC. |
| HDMI cable | Target PC to the capture dongle. |

### Ports on the STB

| Port | Controller | Use |
| --- | --- | --- |
| USB 3.0 | dwc3 (xHCI) | Host. The hub and the capture dongle hang off it. |
| USB 2.0 Type-A | dwc2 OTG (`ff400000.usb`) | Device. It presents the keyboard, mouse and CD-ROM to the target PC. |

```text
dwc3 USB 3.0 host port
└── MACROSILICON 534d:2109 capture dongle (UVC)
dwc2 USB 2.0 OTG port ── A-to-A cable ──> target PC
```

### Kernel requirements

The gadget needs these kernel options, all present in the tested kernel:
`CONFIG_USB_DWC2`, `CONFIG_USB_CONFIGFS`, `CONFIG_USB_CONFIGFS_F_HID`, `CONFIG_USB_CONFIGFS_MASS_STORAGE`, `CONFIG_USB_ROLE_SWITCH` and `CONFIG_USB_VIDEO_CLASS`.

### Known hardware limits

- The STB's built-in Ethernet port is 100 Mbps. A 1080p60 MJPEG stream can need more than that, so lower the resolution or frame rate if the picture stutters.
- The STB's USB power is weak. If devices drop out, use a self-powered hub.
- The board has no video input. The capture dongle is required.

### Device tree

`hg680-fj/` holds the device tree of the board:

| File | Size | What it is |
| --- | --- | --- |
| `meson-g12a-hg680-fj.dtb` | 75,627 bytes | The compiled device tree blob get from [ophub/amlogic-s9xxx-armbian](https://github.com/ophub/amlogic-s9xxx-armbian) |
| `meson-g12a-hg680-fj.dts` | 97,594 bytes | The same tree as source text. |

These files are a hardware reference. The build does not use them and the package does not install them. The model is `HG680-FJ`, compatible with `hg680,fj` and `amlogic,g12a`. The `.dts` is plain decompiler output with numeric phandles and no comments, and `dtc` 1.7.2 compiles it back to the `.dtb` byte for byte.

The nodes that matter to KVMoSTB:

| Node | Setting |
| --- | --- |
| `usb@ffe09000` | `amlogic,meson-g12a-usb-ctrl` glue with `dr_mode = "otg"`. VBUS comes from the `USB_PWR_EN` regulator. |
| `usb@ff400000` | dwc2 with `dr_mode = "peripheral"`. This is the gadget port. |
| `usb@ff500000` | dwc3 with `dr_mode = "host"`. The capture dongle plugs into it. |
| `opp-2016000000` | A 2.016 GHz CPU operating point. |

The STB in this project boots its own copy of the tree from `/boot/dtb/amlogic/`, and that copy is a different build of 80,323 bytes. Its USB nodes use the same modes, but node names and some properties differ, for example `sd@` in this reference against `mmc@` in the booted tree. Treat the files here as a reference, not as a replacement for what your STB boots. To read the tree your STB runs, or to check these files:

```sh
dtc -I dtb -O dts /boot/dtb/amlogic/meson-g12a-hg680-fj.dtb              # the running tree
dtc -I dts -O dtb -o /tmp/check.dtb hg680-fj/meson-g12a-hg680-fj.dts     # compile the reference
```

`dtc` prints warnings when it compiles the reference and still exits 0.

The board's device tree was added upstream in [ophub/amlogic-s9xxx-armbian#3089](https://github.com/ophub/amlogic-s9xxx-armbian/pull/3089).

## Screenshots

| Login | Console |
| --- | --- |
| <img src="screenshots/login.png" width="640" alt="The login page, signed out"> |  <img src="screenshots/console.png" width="640" alt="The web console with the live video stream"> |

| On-screen keyboard | Virtual media |
| --- | --- |
| <img src="screenshots/keyboard.png" width="640" alt="The on-screen keyboard card, open"> | <img src="screenshots/virtual-media-mounted.png" width="640" alt="The virtual media card with an ISO mounted"> |

More captures in [`screenshots`](screenshots/)

## How it works

### Video

The MS2109 turns the target's HDMI output into a USB video stream. `kvmostb` opens it through V4L2 and serves the MJPEG frames as a multipart HTTP stream, which the browser shows in an `<img>` tag. The resolution menu is the fixed list above intersected with what the dongle advertises, so the 25 fps option exists only at 1080p. One client streams at a time, and a new client takes over from the old one.

### Keyboard and mouse

The STB's OTG port runs a USB gadget built through configfs: a boot keyboard with an 8-byte report and an absolute mouse with a 6-byte report. The browser sends each key and mouse event as JSON over a WebSocket (`/ws/hid`). `internal/hid` turns it into a report, and a per-device writer goroutine writes the report to `/dev/hidg0` or `/dev/hidg1`. A write to those files blocks until the target polls the endpoint, so each device has its own bounded queue. A full queue drops the oldest report, and one stuck device cannot stall the other.

Browsers send mouse coordinates as 12-bit values (0-4095). The gadget uses 16-bit values (0-32767), so the mouse code rescales them.

### Virtual media

The gadget starts with no mass-storage function. Mounting an ISO from the console adds a `mass_storage` function with the image as its backing file and rebinds the UDC, so the target sees a new USB CD-ROM. Unmounting removes the function the same way. The search roots are `/mnt/shared/iso` (NFS) and `ISO_LOCATION_DIR`.

### Sign-in

`POST /api/login` checks `ADMIN_USER` and `ADMIN_PASSWORD` in constant time and sets an `HttpOnly` session cookie that lasts 12 hours. Every route except `/healthz`, the login page and its static assets needs that cookie. API and WebSocket routes answer 401 without it, and other routes redirect to `/login`.

### Start-up

| Unit | Job |
| --- | --- |
| `kvmostb-gadget.service` | A oneshot that runs `kvmostb -gadget-init`. It retries up to five times, because the first attempt can fail with `EBUSY` while stale configfs state still holds the UDC. |
| `kvmostb.service` | The daemon. It starts after the gadget unit and restarts on failure. |

The gadget code first writes `device` to `/sys/class/usb_role/*/role`. On the S905X2 the dwc2 and dwc3 controllers share one USB 2 PHY. Setting `dr_mode` on the dwc2 node does not switch the PHY, so without this write the link never reaches `configured`.

```mermaid
sequenceDiagram
    participant B as Browser
    participant S as kvmostb
    participant G as USB gadget
    participant T as Target PC

    B->>S: POST /api/login
    S-->>B: session cookie
    B->>S: GET / and /api/video/stream
    S-->>B: console and MJPEG frames
    B->>S: WebSocket /ws/hid
    B->>S: key or mouse event (JSON)
    S->>G: HID report to /dev/hidg0 or /dev/hidg1
    G->>T: USB interrupt transfer
    B->>S: POST /api/iso/mount?file=...
    S->>G: add mass_storage, rebind UDC
    G->>T: new USB CD-ROM appears
```

### API

| Route | Method | Purpose |
| --- | --- | --- |
| `/healthz` | GET | Liveness probe, no sign-in. |
| `/api/login`, `/api/logout` | POST | Start and end a session. |
| `/api/status` | GET | Video, USB state and mounted ISO. |
| `/api/video/stream` | GET | MJPEG stream. |
| `/api/video/screenshot` | GET | One JPEG frame. |
| `/api/video/resolutions` | GET | The resolution and frame-rate menu. |
| `/api/video/resolution` | POST | Set `width`, `height` and `fps`. |
| `/ws/hid` | WebSocket | Keyboard and mouse events. |
| `/api/hid/reset` | POST | Release every key and button. |
| `/api/hid/jiggler` | GET, POST | Read or set the mouse jiggler. |
| `/api/iso/list` | GET | ISO files in the search roots. |
| `/api/iso/status` | GET | Mounted ISO and USB state. |
| `/api/iso/mount` | POST | Mount `?file=<path>`; with no `file`, unmount. |

## Build

You need Go 1.24 or newer. The three Go dependencies are vendored in `third_party/`, so the build needs no network.

`go4vl` uses cgo for the V4L2 headers, so building for arm64 from an amd64 machine needs a cross compiler. On Debian or Ubuntu:

```sh
sudo apt-get install gcc-aarch64-linux-gnu libc6-dev-arm64-cross linux-libc-dev-arm64-cross dpkg
```

Run the tests, then build the package:

```sh
go test ./...
./packaging/build-deb.sh                  # dist/kvmostb_1.0.0_arm64.deb
VERSION=1.0.1 ./packaging/build-deb.sh    # another version
```

To build on the STB itself, install `gcc` and run `go build -o kvmostb ./cmd/kvmostb`.

### Releases

Pushing a tag such as `v1.0.0` starts `.github/workflows/release.yml`. It runs the tests, builds `kvmostb_1.0.0_arm64.deb`, checks that the binary is arm64, and attaches the package and its SHA-256 checksum to a GitHub release.

```sh
git tag v1.0.0
git push origin v1.0.0
```

The workflow can also run by hand from the Actions tab with a version number. That run keeps the package as a build artifact and does not publish a release.

## Install

1. Download `kvmostb_<version>_arm64.deb` from the [releases page](https://github.com/vikipranata/KVMoSTB/releases) and copy it to the STB.
2. Install it. `dpkg` pulls in `kmod` and `v4l-utils` if `apt` resolves the dependencies:

   ```sh
   sudo apt install ./kvmostb_1.0.0_arm64.deb
   ```

3. Set the credentials. The service refuses every sign-in until both are set:

   ```sh
   sudoedit /etc/kvmostb/kvmostb.env      # set ADMIN_USER and ADMIN_PASSWORD
   sudo systemctl restart kvmostb
   ```

4. Cable it up: the capture dongle in the USB 3.0 port, HDMI from the target PC to the dongle, and the A-to-A cable from the STB's USB 2.0 port to the target.
5. Open `http://<stb-address>:8080` and sign in.

To put ISO images on the STB, copy them to `/var/lib/kvmostb/iso`, or mount an NFS share at `/mnt/shared/iso`.

Check the services with:

```sh
systemctl status kvmostb-gadget kvmostb
journalctl -u kvmostb -f
```

Upgrading is the same `apt install ./kvmostb_<new>.deb`. It keeps `/etc/kvmostb/kvmostb.env` and restarts the service.

## Configuration

`/etc/kvmostb/kvmostb.env` is read by `kvmostb.service`.

| Variable | Default | Meaning |
| --- | --- | --- |
| `ADMIN_USER` | none | Sign-in user. Required. |
| `ADMIN_PASSWORD` | none | Sign-in password. Required. |
| `LISTEN_ADDRESS` | `0.0.0.0` | Address to listen on. |
| `LISTEN_PORT` | `8080` | Port to listen on. |
| `ISO_AUTO_MOUNT` | `false` | Set to `true` to mount the newest ISO at boot. |
| `ISO_LOCATION_DIR` | `/var/lib/kvmostb/iso` | Local ISO folder. `/mnt/shared/iso` is always searched first. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |

The binary also takes `-video`, `-listen`, `-jpeg-quality` and `-log-level` flags. `-video` is auto-detected when empty, so the dongle keeps working if its `/dev/videoN` number changes.

## Limitations

- The console speaks plain HTTP and keeps sessions in memory. Sessions end when the service restarts, and login attempts are not rate limited. Put the console behind a VPN or a reverse proxy with TLS before you expose it beyond your LAN.
- The mouse is absolute and declared as a boot mouse. BIOS or UEFI firmware that binds the boot-mouse protocol may misread it. Operating systems are unaffected. This has not been tested on real firmware.
- Typing into a Linux text console works only when that console is the foreground virtual terminal. Run `sudo chvt 1` on the target if text goes nowhere.
- The capture dongle has no audio, so there is no sound.
- The 1.0.0 release was tested with the STB cabled to itself, not against a separate target PC. See [CHANGELOG.md](CHANGELOG.md) for the details.

## Credits

KVMoSTB started from ideas in the projects below. Thank you to their authors.

| Project | Used for | License |
| --- | --- | --- |
| [DezKVM-Go](https://github.com/tobychui/DezKVM-Go) by Toby Chui | The starting point: the `/ws/hid` input protocol and the MS2109 capture approach. | GPL-3.0 |
| [go4vl](https://github.com/vladimirvivien/go4vl) by Vladimir Vivien | V4L2 capture. Vendored in `third_party/`. | MIT |
| [gorilla/websocket](https://github.com/gorilla/websocket) | The `/ws/hid` WebSocket. Vendored in `third_party/`. | BSD-2-Clause |
| [golang.org/x/sys](https://pkg.go.dev/golang.org/x/sys) | System calls used by go4vl. Vendored in `third_party/`. | BSD-3-Clause |
| [simple-keyboard](https://github.com/hodgef/simple-keyboard) by Francisco Hodge | The layout pattern of the on-screen keyboard. | MIT |
| [noVNC](https://github.com/novnc/noVNC) | The local-scaling behavior of fullscreen. | See its repository |
| [ophub/amlogic-s9xxx-armbian](https://github.com/ophub/amlogic-s9xxx-armbian) | The Armbian image and the device tree for the HG680-FJ. | See its repository |

Technology: [Go](https://go.dev), the Linux USB gadget framework (configfs, `f_hid`, `f_mass_storage`), V4L2 and [Mermaid](https://mermaid.js.org) for the diagrams.

## License

KVMoSTB is free software under the [GNU General Public License v3.0](LICENSE), or any later version.
