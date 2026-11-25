# AGENTS.md

Rules for AI agents and contributors working in this repository. Read this first.

## Project

KVMoSTB is a KVM-over-IP appliance for the Telkom IndiHome HG680-FJ set-top box (Amlogic S905X2, arm64). It is one Go binary with the web console embedded, two systemd units and a `.deb` package.

- Video: MS2109 USB capture dongle, V4L2 MJPEG, served over HTTP.
- Input: USB HID gadget on the dwc2 OTG port (configfs), keyboard and absolute mouse.
- Virtual media: `f_mass_storage` CD-ROM backed by an ISO file.
- Console: vanilla HTML and JS in `cmd/kvmostb/web/`, no framework, no CDN.

## Layout

| Path | Contents |
| --- | --- |
| `cmd/kvmostb/` | Entry point, routing, session auth, embedded `web/` console. |
| `internal/capture/` | V4L2 capture, fixed resolution menu, MJPEG stream. |
| `internal/gadget/` | configfs gadget, UDC bind and unbind, ISO swap, async `/dev/hidg*` writer. |
| `internal/hid/` | Key and mouse state, boot-keyboard and absolute-mouse reports, `/ws/hid` handler. |
| `internal/iso/` | ISO search roots. |
| `internal/logging/` | Leveled logger. |
| `packaging/` | `build-deb.sh`, systemd units, `postinst`, env example. |
| `third_party/` | Vendored `go4vl`, `gorilla/websocket`, `x/sys`. |
| `hg680-fj/` | Reference device tree of the board (`.dts` and `.dtb`). Not part of the build. |
| `.github/workflows/release.yml` | Builds and publishes the `.deb` on a `v*.*.*` tag. |

`README.md` holds the hardware notes. `CHANGELOG.md` is the only history file. Do not add `HARDWARE.md`, `PROGRESS.md`, `docs/reports/` or similar.

## Commands

```sh
go test ./...                              # unit tests, no hardware needed
./packaging/build-deb.sh                   # dist/kvmostb_<VERSION>_arm64.deb, VERSION defaults to 1.0.0
KVMOSTB_UI_STUB=1 go test ./cmd/kvmostb/ -run TestWebConsole_ServeStub   # stub console on 127.0.0.1:8097
```

The build needs `gcc-aarch64-linux-gnu`, `libc6-dev-arm64-cross` and `linux-libc-dev-arm64-cross` because `go4vl` uses cgo. It runs with `GOPROXY=off`, so it needs no network.

Never build on the STB: it has little free disk. Build on a dev machine or in CI and copy the `.deb` over.

Check whether `go` exists before you promise a result. If it is missing, say that tests and the build were not run. Never report them as passing.

## Git and release

- Do not `git push`, push a tag, open a pull request or create a release unless the user asks for it in the current conversation. Approval for one push does not cover the next.
- Versions follow [Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html) and tags are `vMAJOR.MINOR.PATCH`. Bump MAJOR for incompatible changes (config keys, API routes, package or service names), MINOR for backward-compatible features, PATCH for backward-compatible fixes. Pre-releases use a suffix such as `v1.1.0-rc.1`.
- A released version is immutable. Never move, delete or re-tag a version that was pushed; ship a new version instead.
- Work on `dev`. Merge to `main` only when the user asks.
- A tag push runs the release workflow, which publishes a public release. Treat it as irreversible.
- Do not commit binaries or build output: `dist/`, `*.deb`, `*.bin`, `*.img`, `.env`. They are in `.gitignore`.
- Write commit messages in English, imperative mood, subject under 72 characters.
- Add a `CHANGELOG.md` entry for every user-visible change, under the next version. State root causes with the evidence, and mark anything not tested.

## Code

- Match the existing style. Keep changes small and aimed at the request.
- Run the code through `gofmt` before committing when Go is available.
- Add no dependency. If one is unavoidable, vendor its source in `third_party/`, add a `replace` line in `go.mod` and credit it in `README.md`. The build must work offline.
- Keep the build reproducible with `CGO_ENABLED=1 GOOS=linux GOARCH=arm64`.
- Keep the web console framework-free and CDN-free. It styles itself only with `shared-css.css`, which is copied verbatim; do not add tokens to it.
- Validate input at trust boundaries. Every route except `/healthz`, `/login`, `/api/login` and the public static assets needs a session. Login must stay fail-closed.
- Never log, print or commit credentials. Do not read `/etc/kvmostb/kvmostb.env` unless a task needs it, and do not echo its values.

## Comments

- Write only comments that add information the code does not show: a reason, a constraint, a protocol rule, a workaround or a silent failure.
- One line, two at most. Cut issue numbers, version history and "because X so Y" chains.
- Remove banners, step narration, comments that restate the next line, end-of-block markers and vague TODOs.
- Keep licence and copyright notices.
- A comment-only change must leave the code identical. Compare the code with comments stripped before you commit.

## Documentation

- Write in English, in plain sentences with a natural voice.
- No em dashes, buzzwords, forced lists of three, "not just X, it's Y" lines or announcements such as "let's dive in".
- Do not invent facts, numbers, versions, authors or licences. Say "not tested" when something was not tested.
- Put a figure in a document only when you measured it or can cite where it came from.
- Screenshots live in `screenshots/` and are captured by the maintainer or by a read-only test run. Do not edit or replace them, and check each one for credentials, tokens and private addresses before adding it to `README.md`.
- Do not document hardware the project does not use or depend on.

## Hardware and kernel rules

These cost real debugging time. Do not undo them.

1. **Role switch.** Write `device` to `/sys/class/usb_role/*/role` before binding the UDC. `dr_mode` on the dwc2 node does not arm the shared PHY. The attribute accepts only `none`, `host` and `device`; `peripheral` fails with `EINVAL`.
2. **Report descriptors.** Every Input and Output item needs a `Report Size` (`0x75`). Without it the host parses 0-bit fields and fails every report with `-75`. `TestReportDescriptorsMatchReportLength` guards this. Change the descriptor and `report_length` together.
3. **UDC unbind.** Write a newline, not zero bytes. A zero-length write fails with `-EFAULT` and does not unbind. Check the state after the write.
4. **UDC bind.** Retry on `EBUSY`; dwc2 needs a moment to release the port.
5. **Mass-storage LUN order.** Write `ro`, `cdrom` and `removable` first and `file` last. Writing `file` activates the LUN and later writes fail with `EBUSY`.
6. **ISO swap.** Destroy and recreate the mass-storage function. Re-pointing `file` fails with `EPERM` after the first bind.
7. **`/dev/hidg*` handles.** A UDC rebind recreates the nodes and old handles accept writes silently. Reopen after a rebind and compare inodes before each write.
8. **HID writes block.** A write to `/dev/hidgN` blocks until the host polls the endpoint. Keep one writer goroutine and one bounded queue per device, with drop-oldest. Never write to a hidg file from a request goroutine or under a shared lock.
9. **Teardown order.** Unbind the UDC, remove the config symlinks, then `rmdir` the gadget root. configfs group directories refuse `rmdir`.
10. **Frame discard.** The first MJPEG frame from the MS2109 is dropped because Chrome cannot decode it.

## Testing

- New logic gets a test. A bug fix gets a regression test that fails without the fix.
- Tests must not need hardware, root or the network. Skip or fake the device.
- Do not add a test that blocks or waits for a file or a human; gate it behind an environment variable.
- Most behaviour here needs a real STB. When you cannot run it, list the exact steps to verify on hardware and say that it was not verified.
- Hardware checks on a live STB are read-only unless the user approves a change. Sending HID reports moves a real keyboard and mouse, so say so first and leave no key or button held.

## Packaging

- The package is `kvmostb`, the binary is `/usr/bin/kvmostb`, the config is `/etc/kvmostb/kvmostb.env`, and ISOs live in `/var/lib/kvmostb/iso`.
- `postinst` must never overwrite an existing `kvmostb.env`, and keeps it mode 600.
- `kvmostb.service` depends on `kvmostb-gadget.service`. Keep the gadget retry loop.
- The version comes from the tag: `v1.2.3` builds `kvmostb_1.2.3_arm64.deb`. Do not hard-code a version outside `build-deb.sh`.

## Before you hand work back

1. The change does what was asked and nothing else.
2. `go test ./...` passed, or you said it could not run.
3. Comments and documents follow the rules above.
4. `CHANGELOG.md` is updated.
5. Nothing was pushed, tagged or published without approval.
6. You listed what you did not verify.
