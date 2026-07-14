#!/usr/bin/env bash
#
# build-qemu-macos.sh — build a self-contained, virgl-capable qemu-system-aarch64
# bundle for vee on Apple Silicon macOS, and package it as the
# qemu-system-aarch64-darwin-arm64.tar.gz release asset.
#
# The produced bundle has the layout vee's qemubin package extracts into ~/.vee:
#
#   bin/qemu-system-aarch64        (rpath -> @loader_path/../lib)
#   lib/*.dylib                    (ANGLE, virglrenderer, epoxy, MoltenVK, deps)
#   share/qemu/...                 (datadir incl. edk2-aarch64-code.fd, vars)
#
# Why a bundle: stock/Homebrew QEMU on macOS has no virglrenderer, so guest 3D is
# software-only. Accelerated virtio-gpu needs QEMU built --enable-virglrenderer
# against a macOS-patched virglrenderer + ANGLE (GLES->Metal), plus MoltenVK for
# Venus/Vulkan. None of that is on a stock macOS system, so we vendor it.
#
# This mirrors the established UTM / knazarov(qemu-virgl) / startergo recipe. It
# must run on an Apple Silicon macOS runner (e.g. GitHub's macos-15). It is the
# load-bearing, hard-to-test step — expect to iterate the ANGLE/virgl pinning.
#
# Usage: QEMU_VERSION=10.0.2 VEE_SUFFIX=vee1 ./scripts/build-qemu-macos.sh
set -euo pipefail

QEMU_VERSION="${QEMU_VERSION:?set QEMU_VERSION, e.g. 10.0.2}"
VEE_SUFFIX="${VEE_SUFFIX:-vee1}"
WORK="${WORK:-$(pwd)/qemu-build}"
OUT="${OUT:-$(pwd)/dist}"
ASSET="qemu-system-aarch64-darwin-arm64"
JOBS="$(sysctl -n hw.ncpu)"

if [[ "$(uname -s)" != "Darwin" || "$(uname -m)" != "arm64" ]]; then
  echo "error: this script must run on an Apple Silicon (arm64) macOS host" >&2
  exit 1
fi

echo "==> Installing build dependencies via Homebrew"
brew update
# Base QEMU build deps.
brew install meson ninja pkg-config glib pixman dtc capstone libslirp \
  jpeg-turbo libpng curl ncurses dylibbundler bzip2

# virglrenderer + ANGLE (GLES->Metal) + patched libepoxy come from the
# qemu-virgl tap, which carries Akihiko Odaki's not-yet-upstream macOS GL patches.
# MoltenVK provides host Vulkan for the Venus path.
brew tap knazarov/qemu-virgl || true
# Homebrew >= 6 requires explicit trust for third-party taps before it will load
# their formulae — and while an untrusted tap is present it also refuses to
# resolve the plain-core fallback below. Trust this specific tap (scoped, not the
# broad HOMEBREW_NO_REQUIRE_TAP_TRUST); a no-op on older Homebrew without trust.
brew trust knazarov/qemu-virgl 2>/dev/null || true
# The tap's patched libepoxy is named "libepoxy-angle" (it carries the ANGLE
# GLES->Metal patches), not "libepoxy".
brew install knazarov/qemu-virgl/libangle \
  knazarov/qemu-virgl/libepoxy-angle \
  knazarov/qemu-virgl/virglrenderer || {
    echo "warning: qemu-virgl tap formulae unavailable; falling back to plain libepoxy/virglrenderer (NO macOS GL accel)" >&2
    brew install libepoxy virglrenderer
  }
brew install molten-vk vulkan-headers || true

BREW_PREFIX="$(brew --prefix)"
# Collect pkg-config paths for the (possibly cellar-pinned) GL deps.
PKGS="$BREW_PREFIX/lib/pkgconfig:$BREW_PREFIX/share/pkgconfig"
for f in libangle libepoxy-angle virglrenderer; do
  p="$(brew --prefix "$f" 2>/dev/null || true)"
  [[ -n "$p" ]] && PKGS="$p/lib/pkgconfig:$PKGS"
done
export PKG_CONFIG_PATH="$PKGS"

# QEMU's configure creates a Python venv (mkvenv) that needs "distlib" to build
# console-script wrappers. Some python.org framework builds ship pip without a
# usable standalone distlib, which makes configure abort with "found no usable
# distlib". Ensure it up front against the same python3 configure will pick
# (harmless if already present).
python3 -m pip install --user distlib >/dev/null 2>&1 || true

echo "==> Fetching QEMU $QEMU_VERSION"
mkdir -p "$WORK" && cd "$WORK"
curl -fsSL "https://download.qemu.org/qemu-${QEMU_VERSION}.tar.xz" -o qemu.tar.xz
rm -rf "qemu-${QEMU_VERSION}"
tar xf qemu.tar.xz
cd "qemu-${QEMU_VERSION}"

echo "==> Configuring QEMU (cocoa + opengl + virglrenderer + hvf, aarch64-softmmu)"
# libangle ships no pkg-config file, and libepoxy-angle's headers #include
# <EGL/...> from libangle, so PKG_CONFIG_PATH alone leaves QEMU unable to find
# EGL/eglplatform.h. QEMU 10.x also does not thread configure's --extra-cflags
# through to its ui/egl-*.c objects, so pass the ANGLE/epoxy/virgl include and
# lib dirs through CPATH/LIBRARY_PATH (which clang always honors) — plus the
# matching --extra-* flags, mirroring the knazarov qemu-virgl formula.
GLFLAGS=""
for f in libangle libepoxy-angle virglrenderer; do
  p="$(brew --prefix "$f" 2>/dev/null || true)"
  [[ -z "$p" ]] && continue
  GLFLAGS="$GLFLAGS --extra-cflags=-I$p/include --extra-ldflags=-L$p/lib"
  CPATH="${CPATH:+$CPATH:}$p/include"
  LIBRARY_PATH="${LIBRARY_PATH:+$LIBRARY_PATH:}$p/lib"
done
export CPATH LIBRARY_PATH
# Homebrew prefixes contain no spaces, so word-splitting $GLFLAGS is intentional.
# shellcheck disable=SC2086
./configure \
  --prefix=/usr/local \
  --target-list=aarch64-softmmu \
  --enable-cocoa \
  --enable-opengl \
  --enable-virglrenderer \
  --enable-hvf \
  --enable-slirp \
  --enable-curl \
  --disable-docs \
  --disable-debug-info \
  $GLFLAGS

echo "==> Building"
make -j"$JOBS"
STAGE="$WORK/stage"
rm -rf "$STAGE"
make install DESTDIR="$STAGE"

echo "==> Assembling bundle"
BUNDLE="$WORK/bundle"
rm -rf "$BUNDLE"
mkdir -p "$BUNDLE/bin" "$BUNDLE/lib" "$BUNDLE/share"
cp "$STAGE/usr/local/bin/qemu-system-aarch64" "$BUNDLE/bin/"
# datadir (pc-bios, keymaps, and the decompressed edk2 aarch64 firmware).
cp -R "$STAGE/usr/local/share/qemu" "$BUNDLE/share/qemu"

# Ensure the edk2 ARM firmware vee probes for is present and decompressed.
for fw in edk2-aarch64-code edk2-arm-vars; do
  if [[ -f "$BUNDLE/share/qemu/${fw}.fd.bz2" && ! -f "$BUNDLE/share/qemu/${fw}.fd" ]]; then
    bunzip2 -k "$BUNDLE/share/qemu/${fw}.fd.bz2"
  fi
done

echo "==> Bundling dylibs (dylibbundler) and fixing rpaths"
dylibbundler --overwrite-files --bundle-deps --create-dir \
  --fix-file "$BUNDLE/bin/qemu-system-aarch64" \
  --dest-dir "$BUNDLE/lib" \
  --install-path "@loader_path/../lib"

# MoltenVK for Venus/Vulkan: copy the dylib + an ICD manifest the guest-facing
# host Vulkan loader can find relative to the bundle.
MVK="$(brew --prefix molten-vk 2>/dev/null || true)"
if [[ -n "$MVK" && -f "$MVK/lib/libMoltenVK.dylib" ]]; then
  cp "$MVK/lib/libMoltenVK.dylib" "$BUNDLE/lib/"
  mkdir -p "$BUNDLE/share/vulkan/icd.d"
  cat > "$BUNDLE/share/vulkan/icd.d/MoltenVK_icd.json" <<'JSON'
{ "file_format_version": "1.0.0", "ICD": { "library_path": "../../../lib/libMoltenVK.dylib", "api_version": "1.2.0" } }
JSON
fi

echo "==> Code signing (ad-hoc) with hypervisor entitlement"
ENTITLEMENTS="$(cd "$(dirname "$0")/.." && pwd)/internal/qemubin/qemu-entitlements.plist"
# Sign dylibs first, then the main binary last (so its signature stays valid).
find "$BUNDLE/lib" -name '*.dylib' -exec codesign --force --sign - --timestamp=none {} \;
codesign --force --sign - --entitlements "$ENTITLEMENTS" --timestamp=none \
  "$BUNDLE/bin/qemu-system-aarch64"
codesign --verify --verbose "$BUNDLE/bin/qemu-system-aarch64"

echo "==> Writing GPLv2 compliance files (COPYING + SOURCE.txt)"
# QEMU is GPLv2-only; publishing this bundle distributes QEMU binaries, so ship
# the license text and a corresponding-source pointer. This build patches QEMU's
# GL stack via the knazarov/qemu-virgl tap, so flag it as patched.
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
QEMU_PATCHES="virglrenderer + ANGLE (GLES->Metal) GL patches from the knazarov/qemu-virgl Homebrew tap" \
  bash "$SCRIPT_DIR/qemu-bundle-license.sh" "$BUNDLE" "$WORK/qemu-${QEMU_VERSION}" "$QEMU_VERSION" \
    --target-list=aarch64-softmmu --enable-cocoa --enable-opengl --enable-virglrenderer \
    --enable-hvf --enable-slirp --enable-curl --disable-docs --disable-debug-info

echo "==> Packaging $ASSET.tar.gz"
mkdir -p "$OUT"
( cd "$BUNDLE" && tar czf "$OUT/$ASSET.tar.gz" bin lib share )
( cd "$OUT" && shasum -a 256 "$ASSET.tar.gz" | tee "$ASSET.tar.gz.sha256" )

# INSTALL_LOCAL=1 drops the freshly built bundle straight into ~/.vee so vee
# uses it immediately — no GitHub release, no version.go edit. qemubin's
# resolveSystemQemu prefers the non-versioned ~/.vee/bin/<binary> over Homebrew,
# so this is the fast path for testing a local build on the same machine.
if [[ "${INSTALL_LOCAL:-0}" == "1" ]]; then
  VEE_ROOT="${VEE_ROOT:-$HOME/.vee}"
  echo "==> Installing locally into $VEE_ROOT (INSTALL_LOCAL=1)"
  mkdir -p "$VEE_ROOT"
  ( cd "$VEE_ROOT" && tar xzf "$OUT/$ASSET.tar.gz" )
  echo "    Installed $VEE_ROOT/bin/qemu-system-aarch64 — vee will pick it up automatically."
fi

echo "==> Done. Asset: $OUT/$ASSET.tar.gz"
echo "    For a one-machine local install, re-run with INSTALL_LOCAL=1 (drops the"
echo "    bundle into ~/.vee; no release needed)."
echo "    To publish for all users, update internal/qemubin/version.go:"
echo "      PinnedVersion = \"qemu-${QEMU_VERSION}-${VEE_SUFFIX}\""
echo "      Checksums[\"darwin-arm64\"] = \"$(cut -d' ' -f1 "$OUT/$ASSET.tar.gz.sha256")\""
