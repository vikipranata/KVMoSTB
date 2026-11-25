#!/usr/bin/env bash
# Build dist/kvmostb_<version>_arm64.deb.
#
#   ./packaging/build-deb.sh                  # version 1.0.0
#   VERSION=1.2.3 ./packaging/build-deb.sh
#
# go4vl uses cgo, so cross-compiling from amd64 needs gcc-aarch64-linux-gnu,
# libc6-dev-arm64-cross and linux-libc-dev-arm64-cross. Set CC to use another compiler.
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${VERSION:-1.0.0}"
MAINTAINER="${MAINTAINER:-Viki Pranata <helovikipranata@gmail.com>}"
ARCH=arm64
DIST=dist
PKG="kvmostb_${VERSION}_${ARCH}"
ROOT="$DIST/$PKG"

# The dependencies are vendored under third_party/, so the build needs no network.
export CGO_ENABLED=1 GOOS=linux GOARCH=arm64 GOPROXY=off GOFLAGS=-mod=mod
export CC="${CC:-aarch64-linux-gnu-gcc}"

rm -rf "$ROOT"
mkdir -p "$ROOT/DEBIAN" "$ROOT/usr/bin" "$ROOT/lib/systemd/system" \
	"$ROOT/etc/kvmostb" "$ROOT/var/lib/kvmostb/iso"

echo "==> building kvmostb $VERSION ($ARCH)"
go build -trimpath -ldflags "-s -w" -o "$ROOT/usr/bin/kvmostb" ./cmd/kvmostb
chmod 0755 "$ROOT/usr/bin/kvmostb"

install -m 0644 packaging/kvmostb-gadget.service packaging/kvmostb.service "$ROOT/lib/systemd/system/"
install -m 0644 packaging/kvmostb.env.example "$ROOT/etc/kvmostb/"
install -m 0755 packaging/postinst "$ROOT/DEBIAN/postinst"

cat > "$ROOT/DEBIAN/control" <<CONTROL
Package: kvmostb
Version: $VERSION
Architecture: $ARCH
Maintainer: $MAINTAINER
Section: misc
Priority: optional
Depends: kmod, v4l-utils
Homepage: https://github.com/vikipranata/KVMoSTB
Description: KVM-over-IP console for the HG680-FJ set-top box
 Streams the target PC's HDMI output from a USB capture dongle, sends keyboard
 and mouse input through a USB HID gadget, and mounts ISO images as a USB
 CD-ROM. The web console signs in with a form login and fails closed.
CONTROL

echo "==> packaging $ROOT.deb"
dpkg-deb --build --root-owner-group "$ROOT" >/dev/null
echo "==> done: $ROOT.deb"
