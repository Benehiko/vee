#!/usr/bin/env bash
#
# build-qemu-windows.sh — cross-build a WHPX-capable qemu-system-x86_64.exe
# bundle for vee on Windows (amd64), packaged as the
# qemu-system-x86_64-windows-amd64.tar.gz release asset.
#
# QEMU for Windows is cross-compiled from Linux with the MinGW-w64 toolchain
# (x86_64-w64-mingw32). The Windows Hypervisor Platform accelerator (--enable-whpx)
# is a Windows-only backend that the MinGW target supports. The produced bundle
# has the same layout vee's qemubin package extracts into ~/.vee:
#
#   bin/qemu-system-x86_64.exe     the emulator
#   bin/*.dll                      MinGW runtime + dependency DLLs it needs
#   share/qemu/...                 datadir (pc-bios, keymaps, firmware)
#   share/licenses/qemu/           GPLv2 COPYING + SOURCE.txt (compliance)
#
# QEMU is GPLv2-only; this script ships the license text and a corresponding-
# source pointer in the bundle (see qemu-bundle-license.sh).
#
# Runs on a Linux host with the MinGW-w64 cross toolchain. Intended for the
# ubuntu-latest CI runner via a container; also runnable locally on Debian/Ubuntu.
#
# Usage: QEMU_VERSION=10.0.2 VEE_SUFFIX=vee1 ./scripts/build-qemu-windows.sh
set -euo pipefail

QEMU_VERSION="${QEMU_VERSION:?set QEMU_VERSION, e.g. 10.0.2}"
VEE_SUFFIX="${VEE_SUFFIX:-vee1}"
WORK="${WORK:-$(pwd)/qemu-build-win}"
OUT="${OUT:-$(pwd)/dist}"
ASSET="qemu-system-x86_64-windows-amd64"
JOBS="$(nproc)"
# Resolve the script dir up front, before any cd, so the license helper is found
# regardless of the working directory later in the script.
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

CROSS=x86_64-w64-mingw32
# Fedora's mingw64 packages install the target sysroot here (Debian used
# /usr/${CROSS}, but Debian/Ubuntu do not package mingw glib so we build on
# Fedora — see below).
MINGW_SYSROOT="/usr/${CROSS}/sys-root/mingw"

echo "==> Installing MinGW-w64 cross toolchain and dependencies (Fedora)"
# Ubuntu/Debian do NOT package a MinGW build of GLib (only native libglib2.0-dev),
# so QEMU's cross configure fails with 'Dependency glib-2.0 not found'. Fedora
# ships the full mingw64-* stack and is what QEMU's own upstream CI uses to
# cross-build the Windows binary (tests/docker/dockerfiles/fedora-win64-cross.docker).
# This script therefore expects a Fedora container.
#
# mingw64-pkg-config provides x86_64-w64-mingw32-pkg-config and mingw64-glib2
# provides the target glib-2.0.pc that --cross-prefix resolves. Native gcc/glib2
# + meson/ninja build the host-side codegen tools.
# --skip-unavailable: tolerate a package name that a given Fedora release drops
# or renames (e.g. mingw64-libslirp is absent on some releases — QEMU builds its
# bundled libslirp subproject in that case). --setopt keeps "already installed"
# from aborting the transaction.
dnf -y install --skip-unavailable --setopt=install_weak_deps=False \
  gcc gcc-c++ make ninja-build git python3 python3-pip \
  glib2-devel bzip2 tar xz which findutils diffutils \
  mingw64-gcc mingw64-gcc-c++ mingw64-binutils mingw64-pkg-config \
  mingw64-glib2 mingw64-pixman mingw64-gettext \
  mingw64-SDL2
# meson is pulled via pip to match QEMU's pinned minimum; dnf's may lag.
# --break-system-packages: Fedora's python is PEP 668 externally-managed.
python3 -m pip install --quiet --break-system-packages 'meson>=1.5.0' ||
  dnf -y install meson

echo "==> Fetching QEMU $QEMU_VERSION"
mkdir -p "$WORK" && cd "$WORK"
curl -fsSL "https://download.qemu.org/qemu-${QEMU_VERSION}.tar.xz" -o qemu.tar.xz
rm -rf "qemu-${QEMU_VERSION}"
tar xf qemu.tar.xz
cd "qemu-${QEMU_VERSION}"

# Configure flags shared with the license/source record. WHPX is the headline
# accelerator; slirp gives user-mode networking; SDL2 gives a display window.
# GL/SPICE are omitted from this first Windows bundle because cross-building the
# full virgl/ANGLE stack for Windows is a separate effort — the guest still gets
# accelerated CPU via WHPX and 2D graphics, which is the goal here.
CONFIGURE_FLAGS=(
  --cross-prefix="${CROSS}-"
  --target-list=x86_64-softmmu
  --enable-whpx
  --enable-slirp
  --enable-sdl
  --disable-docs
  --disable-debug-info
)

echo "==> Configuring QEMU for Windows (${CROSS}, WHPX)"
./configure "${CONFIGURE_FLAGS[@]}"

echo "==> Building"
make -j"$JOBS"
STAGE="$WORK/stage"
rm -rf "$STAGE"
make install DESTDIR="$STAGE"

echo "==> Assembling bundle"
BUNDLE="$WORK/bundle"
rm -rf "$BUNDLE"
mkdir -p "$BUNDLE/bin" "$BUNDLE/share"

# QEMU installs into <prefix>/bin (default prefix /usr/local) under DESTDIR.
QEMU_BIN="$(find "$STAGE" -name 'qemu-system-x86_64.exe' -o -name 'qemu-system-x86_64w.exe' | head -n1)"
[[ -z "$QEMU_BIN" ]] && { echo "error: qemu-system-x86_64.exe not found under $STAGE" >&2; exit 1; }
cp "$QEMU_BIN" "$BUNDLE/bin/qemu-system-x86_64.exe"

# Datadir (pc-bios, keymaps, firmware). The install prefix differs by distro
# (Fedora mingw stages it at <stage>/qemu/share, Debian at <stage>/usr/.../share/
# qemu), so locate the datadir by the firmware it must contain rather than by a
# fixed path. Fail loudly if it's missing — a bundle with no firmware is broken.
QEMU_SHARE="$(dirname "$(find "$STAGE" -name 'edk2-x86_64-code.fd' -o -name 'edk2-x86_64-code.fd.bz2' 2>/dev/null | head -n1)")"
if [[ -z "$QEMU_SHARE" || ! -d "$QEMU_SHARE" ]]; then
  echo "error: QEMU datadir (with edk2-x86_64-code.fd) not found under $STAGE" >&2
  exit 1
fi
cp -R "$QEMU_SHARE" "$BUNDLE/share/qemu"

# Decompress the edk2 x86_64 firmware vee probes for (bz2 -> plain .fd). vee's
# runtime extractBundle does not handle bz2, so ship plain .fd. Mirrors the
# aarch64 decompression in build-qemu-macos.sh.
for fw in edk2-x86_64-code edk2-x86_64-secure-code edk2-i386-vars; do
  if [[ -f "$BUNDLE/share/qemu/${fw}.fd.bz2" && ! -f "$BUNDLE/share/qemu/${fw}.fd" ]]; then
    bunzip2 -k "$BUNDLE/share/qemu/${fw}.fd.bz2"
  fi
done

echo "==> Bundling dependency DLLs"
# Resolve the DLLs the .exe imports (MinGW runtime + glib/pixman/etc.) and copy
# them next to the binary so it runs on a stock Windows host. objdump lists the
# PE imports; we recursively pull any that live in the MinGW sysroot.
copy_dlls() {
  local target="$1" seen="$2"
  local dll
  while read -r dll; do
    [[ -z "$dll" ]] && continue
    # Only bundle DLLs we ship (found in the MinGW sysroot); skip Windows system
    # DLLs (kernel32.dll, etc.) that exist on every host.
    local src
    src="$(find "$MINGW_SYSROOT" -iname "$dll" 2>/dev/null | head -n1)"
    [[ -z "$src" ]] && continue
    case " $seen " in *" $dll "*) continue;; esac
    seen="$seen $dll"
    cp -n "$src" "$BUNDLE/bin/"
    # Recurse into the just-copied DLL's own imports.
    copy_dlls "$src" "$seen"
  done < <("${CROSS}-objdump" -p "$target" 2>/dev/null | awk '/DLL Name:/ {print $3}')
  echo "$seen"
}
copy_dlls "$BUNDLE/bin/qemu-system-x86_64.exe" "" >/dev/null
echo "    bundled DLLs:"; ls -1 "$BUNDLE/bin/"*.dll 2>/dev/null || echo "    (none — statically linked)"

echo "==> Writing GPLv2 compliance files (COPYING + SOURCE.txt)"
# Unmodified upstream QEMU (no patches) — omit QEMU_PATCHES.
bash "$SCRIPT_DIR/qemu-bundle-license.sh" "$BUNDLE" "$WORK/qemu-${QEMU_VERSION}" "$QEMU_VERSION" \
  --cross-prefix="${CROSS}-" --target-list=x86_64-softmmu --enable-whpx --enable-slirp \
  --disable-docs --disable-debug-info
# Also ship the MinGW runtime license note alongside (LGPL/GPL-with-exception).
cat > "$BUNDLE/share/licenses/qemu/MINGW-RUNTIME.txt" <<'EOF'
The bundled *.dll files next to qemu-system-x86_64.exe include the MinGW-w64
runtime and cross-built dependency libraries (glib, pixman, SDL2, zlib, etc.).
These are separately licensed (MinGW-w64 runtime: permissive/public-domain with
some LGPL; glib/SDL2: LGPL; zlib: zlib license). Their upstream sources are the
Fedora mingw64-* packages used at build time, obtainable from Fedora's source
RPM archives. See SOURCE.txt for the QEMU corresponding source.
EOF

echo "==> Packaging $ASSET.tar.gz"
mkdir -p "$OUT"
( cd "$BUNDLE" && tar czf "$OUT/$ASSET.tar.gz" bin share )
( cd "$OUT" && sha256sum "$ASSET.tar.gz" | tee "$ASSET.tar.gz.sha256" )

echo "==> Done. Asset: $OUT/$ASSET.tar.gz"
echo "    To publish, update internal/qemubin/version.go:"
echo "      Checksums[\"windows-amd64\"] = \"$(cut -d' ' -f1 "$OUT/$ASSET.tar.gz.sha256")\""
