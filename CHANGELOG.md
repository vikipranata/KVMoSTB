# Changelog

All notable changes to KVMoSTB. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and versions follow [Semantic Versioning](https://semver.org/).

## [1.0.0] - 2026-11-25

First release. The project was developed as miniKVMoIP from 2026-11-19 and renamed
to KVMoSTB on 2026-11-25. The entries below fold the development log and the four
diagnosis reports into one history.

### Added

- Video: MJPEG stream from a MACROSILICON MS2109 (`534d:2109`) capture dongle with a
  fixed menu of 640x480, 1280x720 and 1920x1080 at 25, 30 and 60 fps, cut down to what
  the dongle advertises. The default is 1280x720 at 30 fps. The console shows live
  resolution, frame rate and bitrate, and can take screenshots.
- Input: a USB HID gadget on the dwc2 OTG port presents a boot keyboard and an absolute
  mouse to the target PC. The target needs no software.
- Virtual media: ISO images from the NFS share `/mnt/shared/iso` or `/var/lib/kvmostb/iso`
  mount as a USB CD-ROM through `f_mass_storage`. The console mounts and unmounts with
  one toggle button, and `ISO_AUTO_MOUNT` mounts the newest image at boot.
- Web console: a single page with light and dark themes, a full-size on-screen keyboard
  with sticky Ctrl and Alt, a clipboard paste box that types text into the target, and
  fullscreen with local scaling. The keyboard card hides below 700 px.
- Authentication: form login with an in-memory session cookie (12 hours). Login fails
  closed when `ADMIN_USER` or `ADMIN_PASSWORD` is empty.
- Resilience: a missing capture device leaves input, ISO and the console running with
  video reported offline. The input chip shows online or offline.
- Packaging: a `.deb` for arm64 with two systemd units, `kvmostb-gadget.service` (oneshot,
  retries on `EBUSY`) and `kvmostb.service`. The unit starts at boot and the config is
  copied to `/etc/kvmostb/kvmostb.env` once, mode 600.
- Environment options `LISTEN_ADDRESS`, `LISTEN_PORT`, `ISO_AUTO_MOUNT`, `ISO_LOCATION_DIR`
  and `LOG_LEVEL`.
- A GitHub Actions workflow that builds and publishes the `.deb` when a `v*.*.*` tag is pushed.

### Changed

- Renamed from miniKVMoIP: Go module, binary, package, units, `/etc/kvmostb`,
  `/var/lib/kvmostb`, gadget name, session cookie and UI branding.
- Upstream credits moved out of the product code into the README.
- Comments across the Go, JavaScript, HTML and packaging files were rewritten for the
  release. Code is unchanged.
- The HID report writer runs one goroutine and one 16-slot queue per device. A blocked
  keyboard write can no longer stall the mouse, `ReleaseAll` or `/api/hid/reset`, and a
  full queue drops the oldest report.
- `TestWebConsole_ServeStub` is opt-in (`KVMOSTB_UI_STUB=1`) so `go test ./...` finishes.
- `build-deb.sh` takes the version from `VERSION`, builds with the system cross compiler,
  and declares `kmod` and `v4l-utils` as dependencies.
- The unused `tarm/serial` dependency was dropped.

### Fixed

- **Gadget never reached `configured`.** The shared USB2 PHY (`usb2_phy1`) stayed in host
  mode, because `dr_mode` on the dwc2 node does not arm it. The gadget now writes `device`
  to `/sys/class/usb_role/*/role` before binding. An earlier test wrote `peripheral`, which
  the attribute rejects (it accepts `none`, `host` and `device`), and read that as a dead
  switch.
- **Host saw an `-75` (`EOVERFLOW`) interrupt flood and no input.** The report descriptors
  had no `Report Size` items, so the host parsed 0-bit fields. Both descriptors were
  rewritten as standard boot layouts and `TestReportDescriptorsMatchReportLength` fails
  the build if the descriptor and `report_length` drift apart. The keyboard usage maximum
  was later normalized to the standard 0x63.
- **Input stopped after an ISO mount or unmount.** Rebinding the UDC recreates
  `/dev/hidg0` and `/dev/hidg1`, and the old handles kept accepting writes silently. The
  gadget now reopens the handles after a rebind and compares inodes before each write.
- **ISO mount failed with `EINVAL`, then `EBUSY`.** Unbinding wrote zero bytes to the UDC
  attribute, which the kernel rejects with `-EFAULT` and does not unbind. It now writes a
  newline and checks the result. Separately, the LUN attributes were written in map order,
  and writing `file` activates the LUN, so `ro` and `cdrom` failed about half the time. They
  now go first and `file` last. Mounting also retries the rebind, because dwc2 needs a moment
  to release the port.
- **A stationary mouse click was dropped.** A click is a move frame with a new button mask
  at the same position, and the "no movement" check discarded it. A changed mask now
  forces a report.
- **One unpolled endpoint froze all input.** A write to `/dev/hidgN` blocks until the host
  polls that endpoint, and one shared mutex let it stall the other device and
  `/api/hid/reset`. Fixed by the per-device writer above. A first build of this fix
  deadlocked at startup on a doubled lock and was corrected before release.
- Stuck modifier keys on a real click: sticky Ctrl and Alt ignore `pointerup`.
- `>` typed as `?` by the clipboard paste: it now sends Shift plus period. Uppercase letters
  also send Shift.
- A nil-pointer panic at shutdown when the video device was offline.
- The login page icon redirected to `/login`; static assets and favicons are now public.
- `/favicon.ico` returned 404.
- The first video frame from the MS2109 is discarded because Chrome cannot decode it.

### Known issues

- **Foreground console.** Typing into the target's Linux console reaches the shell only
  when the foreground virtual terminal is the one on screen. In one test it was not, and
  `chvt 1` fixed it. The service does not set it yet. This applies when the target is a
  text console, not a graphical desktop or firmware setup.
- **Absolute mouse in firmware.** The mouse is declared as a boot mouse, whose protocol is
  a fixed 3-byte relative report. BIOS and UEFI that bind the boot protocol may misread
  the 6-byte absolute report. Operating-system drivers are unaffected. This was not tested
  against real firmware.
- **Full speed link.** In a test with an A-A cable the link negotiated full speed (12 Mbps).
  HID works, and ISO transfers are limited to roughly 1.5 MB/s. A later loopback test
  reached high speed, so the cable or the host port is the likely cause.
- **No power-cycle test.** Boot enabling after a cold power cycle was not verified. A reboot
  during the rename migration brought the gadget back cleanly.
- **Not tested on a real target PC.** Video, input and ISO mounting were tested on a
  loopback, with the STB as its own target. Mounting a real bootable ISO was not tested.
- **Audio is not available.** The MS2109 registers a sound card with no capture or playback
  stream, so the console reports audio as unavailable.
- Sessions are lost on restart, the 12 hour TTL never slides, and login attempts are not
  rate limited. Put the console behind a VPN or reverse proxy before exposing it.

### Development notes

- 2026-11-19: PRD and design approved; all hardware spikes done (video, gadget, ISO, network).
  The `go4vl` library uses cgo, so cross builds need an aarch64 C compiler.
- 2026-11-20: capture, HID, gadget, ISO, web console and packaging built and deployed; the
  reboot test passed.
- 2026-11-21 to 2026-11-24: UI polish, form login, virtual keyboard, role-switch fix, descriptor
  fix and ISO mount fixes.
- 2026-11-25: input starvation fix, rename to KVMoSTB, clipboard and keyboard polish.
