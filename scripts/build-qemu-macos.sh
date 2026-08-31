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
# Resolve script/repo paths up front, before any cd, so the entitlements plist
# and the license helper are found regardless of the working directory later.
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

if [[ "$(uname -s)" != "Darwin" || "$(uname -m)" != "arm64" ]]; then
  echo "error: this script must run on an Apple Silicon (arm64) macOS host" >&2
  exit 1
fi

echo "==> Installing build dependencies via Homebrew"
# Best-effort: a failure fetching one unrelated tap must not abort the build;
# the installs below fail on their own if the formulae are truly unavailable.
brew update || echo "warning: brew update failed; continuing with existing formulae" >&2
# Base QEMU build deps.
brew install meson ninja pkg-config glib pixman dtc capstone libslirp \
  jpeg-turbo libpng curl ncurses dylibbundler bzip2

# virglrenderer >= 1.0 + ANGLE (GLES->Metal) + patched libepoxy for accelerated
# virtio-gpu. QEMU 10.x's virtio-gpu-virgl.c requires VIRGL_VERSION_MAJOR >= 1
# (virglrenderer 1.0.0, Oct 2023); the old knazarov/qemu-virgl tap is abandoned
# (last commit 2021) and pins a pre-1.0 virglrenderer, so QEMU 10.x fails to
# compile against it. The maintained successor is the startergo tap family,
# which carries Akihiko Odaki's macOS ANGLE/Metal patches on top of
# virglrenderer 1.x. MoltenVK provides host Vulkan for the Venus path.
#
# If the tap is unavailable we fall back to Homebrew core's virglrenderer, which
# is now >= 1.0 and therefore still COMPILES with QEMU 10.x — but without ANGLE
# it renders in software (no Metal acceleration). We warn in that case.
GL_ACCEL=1
# startergo/virglrenderer/virglrenderer transitively depends (per its formula)
# on startergo/angle/angle, startergo/libepoxy/libepoxy and molten-vk; angle in
# turn build-depends on startergo/gn/gn. So we only need to install the
# virglrenderer formula and Homebrew pulls the rest — but all four taps must be
# tapped and trusted first. Homebrew >= 6 requires explicit per-tap trust before
# it will load third-party formulae, and while an untrusted tap is merely
# *present* it refuses to resolve even core formulae. Trust these four specific
# taps (scoped, not a global trust bypass); a no-op on older Homebrew.
GL_TAPS=(startergo/virglrenderer startergo/angle startergo/gn startergo/libepoxy)
# Taps clone anonymously over https. A user gitconfig that rewrites
# https://github.com/ to ssh would route brew's clone through ssh auth instead
# (and fail wherever the ssh agent is gated) — and brew scrubs the env, so no
# per-process git config can reach its clone. Pre-clone each tap ourselves
# with a longest-match identity rule that beats any such rewrite; brew then
# adopts the existing directory.
export GIT_CONFIG_COUNT=1 \
  GIT_CONFIG_KEY_0="url.https://github.com/startergo/.insteadOf" \
  GIT_CONFIG_VALUE_0="https://github.com/startergo/"
for t in "${GL_TAPS[@]}"; do
  tap_dir="$(brew --repository)/Library/Taps/${t%%/*}/homebrew-${t##*/}"
  if [[ ! -d "$tap_dir" ]]; then
    git clone --depth 1 "https://github.com/${t%%/*}/homebrew-${t##*/}" "$tap_dir" || true
  fi
  brew tap "$t" 2>/dev/null || true
  brew trust "$t" 2>/dev/null || true
done
# Install the GL deps one by one, tolerating per-formula link failures (the
# ANGLE tap bundles vulkan headers that collide with the vulkan-headers
# formula's symlinks): a failed link still leaves a usable keg, but inside a
# single dependent install it aborts the whole chain.
for f in startergo/gn/gn startergo/libepoxy/libepoxy startergo/angle/angle; do
  brew install "$f" 2>/dev/null || true
done
if ! brew install startergo/virglrenderer/virglrenderer; then
  # A failed `brew link` (the ANGLE tap bundles vulkan headers that collide
  # with the vulkan-headers formula) still leaves usable kegs behind, and the
  # include/lib paths below come from `brew --prefix`, which resolves unlinked
  # kegs — only a missing keg means the stack is really unavailable.
  if brew list startergo/virglrenderer/virglrenderer >/dev/null 2>&1; then
    echo "note: virglrenderer keg installed but not linked (header conflicts) — continuing with keg paths" >&2
  else
    # No macOS Homebrew-core virglrenderer exists to fall back to, so build a
    # plain HVF QEMU without accelerated virtio-gpu (2D still works; guests
    # run fast under HVF). Untap the third-party taps so they don't poison
    # core resolution.
    echo "warning: startergo virglrenderer tap unavailable; building WITHOUT accelerated virtio-gpu (HVF + 2D only)" >&2
    brew untap "${GL_TAPS[@]}" 2>/dev/null || true
    brew install libepoxy || true
    GL_ACCEL=0
  fi
fi
brew install molten-vk vulkan-headers || true

BREW_PREFIX="$(brew --prefix)"
# Collect pkg-config paths for the (possibly cellar-pinned) GL deps. Probe both
# the ANGLE-tap formula names and the core fallbacks; missing ones are skipped.
PKGS="$BREW_PREFIX/lib/pkgconfig:$BREW_PREFIX/share/pkgconfig"
for f in angle libangle libepoxy-angle libepoxy virglrenderer; do
  p="$(brew --prefix "$f" 2>/dev/null || true)"
  [[ -n "$p" ]] && PKGS="$p/lib/pkgconfig:$PKGS"
done
export PKG_CONFIG_PATH="$PKGS"

# QEMU's configure creates a Python venv (mkvenv) that needs "distlib" (and
# importable meson/pycotap, else it tries a pip install that a restrictive
# user pip.conf can veto) to build console-script wrappers. Newer runner
# Pythons (3.12+, incl. Homebrew's 3.14) are PEP 668 "externally managed", so
# a bare `pip install --user` fails (and previously did so silently), leaving
# configure to abort with "found no usable distlib". Install into the exact
# python3 configure will pick, tolerating the externally-managed marker and
# overriding pip.conf's require-virtualenv/require-hashes for just this
# install. Try the ordinary path first, then fall back.
PYBIN="$(command -v python3)"
PYPKGS=(distlib meson pycotap)
# Exported for the whole build: configure's mkvenv runs pip itself (qemu.qmp
# et al) and inherits any restrictive user pip.conf otherwise.
export PIP_REQUIRE_VIRTUALENV=false PIP_REQUIRE_HASHES=0
echo "==> Ensuring ${PYPKGS[*]} for $PYBIN ($("$PYBIN" --version 2>&1))"
if ! "$PYBIN" -c 'import distlib, mesonbuild, pycotap' >/dev/null 2>&1; then
  "$PYBIN" -m pip install --user "${PYPKGS[@]}" >/dev/null 2>&1 ||
    "$PYBIN" -m pip install --break-system-packages "${PYPKGS[@]}" >/dev/null 2>&1 ||
    "$PYBIN" -m pip install --user --break-system-packages "${PYPKGS[@]}" >/dev/null 2>&1 ||
    true
fi
"$PYBIN" -c 'import distlib; print("distlib", distlib.__version__)' ||
  echo "warning: distlib still unavailable for $PYBIN; QEMU mkvenv may fail" >&2

echo "==> Fetching QEMU $QEMU_VERSION"
mkdir -p "$WORK" && cd "$WORK"
curl -fsSL "https://download.qemu.org/qemu-${QEMU_VERSION}.tar.xz" -o qemu.tar.xz
rm -rf "qemu-${QEMU_VERSION}"
tar xf qemu.tar.xz
cd "qemu-${QEMU_VERSION}"

# Cocoa OpenGL support: upstream ui/cocoa is 2D-only (no dpy_gl ops), so
# "-display cocoa,gl=es" refuses on every unpatched build. This is Akihiko
# Odaki's cocoa-GL work, via startergo's qemu-v07 rebase, ported to this QEMU
# version. Shipped as a patch-file release asset next to the tarball, like the
# egl-helpers cast below (GPLv2 §3).
COCOA_GL_PATCH="$SCRIPT_DIR/patches/qemu-${QEMU_VERSION}-cocoa-gl.patch"
if [[ -f "$COCOA_GL_PATCH" ]]; then
  echo "==> Applying $(basename "$COCOA_GL_PATCH")"
  patch -p1 --no-backup-if-mismatch < "$COCOA_GL_PATCH"
  mkdir -p "$OUT"
  cp "$COCOA_GL_PATCH" "$OUT/"
else
  echo "warning: no cocoa-GL patch for QEMU ${QEMU_VERSION}; the cocoa display will be 2D-only" >&2
fi

# ui/egl-helpers.c passes an EGLNativeDisplayType straight to
# eglGetPlatformDisplayEXT, whose prototype takes void*. Mesa's eglplatform.h
# defines the native type as a pointer, so upstream never notices; ANGLE's
# defines it as int on macOS, which Xcode 15+ clang rejects outright
# (-Wint-conversion is an error). Cast through uintptr_t — valid for both
# header families, a no-op wherever upstream compiles today.
#
# Keep a pristine copy: the exact change is published as a patch-file release
# asset next to the upstream tarball, so the two together are the complete
# corresponding source for this bundle (GPLv2 §3).
cp ui/egl-helpers.c ui/egl-helpers.c.orig
perl -pi -e \
  's/eglGetPlatformDisplayEXT\(platform, native, NULL\)/eglGetPlatformDisplayEXT(platform, (void *)(uintptr_t)native, NULL)/' \
  ui/egl-helpers.c
if ! grep -q '(void \*)(uintptr_t)native' ui/egl-helpers.c; then
  echo "error: egl-helpers.c ANGLE cast patch did not apply — upstream changed the call site; update the perl expression" >&2
  exit 1
fi
mkdir -p "$OUT"
PATCH_FILE="$OUT/qemu-${QEMU_VERSION}-macos-egl-helpers.patch"
diff -u -L a/ui/egl-helpers.c -L b/ui/egl-helpers.c \
  ui/egl-helpers.c.orig ui/egl-helpers.c > "$PATCH_FILE" && rc=0 || rc=$?
# diff exits 1 when the files differ — anything else means no diff (0) or
# trouble (2), both of which would publish a wrong/empty patch asset.
[[ $rc -eq 1 ]] || { echo "error: egl-helpers.c patch diff failed (diff rc=$rc)" >&2; exit 1; }
rm ui/egl-helpers.c.orig

echo "==> Configuring QEMU (cocoa + opengl + virglrenderer + hvf, aarch64-softmmu)"
# ANGLE ships no pkg-config file, and the patched libepoxy's headers #include
# <EGL/...> from ANGLE, so PKG_CONFIG_PATH alone leaves QEMU unable to find
# EGL/eglplatform.h. QEMU 10.x also does not thread configure's --extra-cflags
# through to its ui/egl-*.c objects, so pass the ANGLE/epoxy/virgl include and
# lib dirs through CPATH/LIBRARY_PATH (which clang always honors) — plus the
# matching --extra-* flags. Probe both tap and core formula names.
GLFLAGS=""
for f in angle libangle libepoxy-angle libepoxy virglrenderer; do
  p="$(brew --prefix "$f" 2>/dev/null || true)"
  [[ -z "$p" ]] && continue
  GLFLAGS="$GLFLAGS --extra-cflags=-I$p/include --extra-ldflags=-L$p/lib"
  CPATH="${CPATH:+$CPATH:}$p/include"
  LIBRARY_PATH="${LIBRARY_PATH:+$LIBRARY_PATH:}$p/lib"
done
export CPATH LIBRARY_PATH
# Only enable the GL/virgl stack when accelerated virglrenderer is available.
# Without it (GL_ACCEL=0) build a plain HVF QEMU — enabling virglrenderer with no
# usable virglrenderer would fail configure.
if [[ "$GL_ACCEL" == "1" ]]; then
  echo "==> Building with ANGLE-accelerated virglrenderer (Metal-backed virtio-gpu)"
  GL_CONFIGURE=(--enable-opengl --enable-virglrenderer)
else
  echo "==> Building WITHOUT accelerated virtio-gpu (HVF + 2D only)"
  GL_CONFIGURE=(--disable-virglrenderer)
  GLFLAGS=""
fi
# Homebrew prefixes contain no spaces, so word-splitting $GLFLAGS is intentional.
# shellcheck disable=SC2086
./configure \
  --prefix=/usr/local \
  --target-list=aarch64-softmmu \
  --enable-cocoa \
  "${GL_CONFIGURE[@]}" \
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

# ANGLE is loaded lazily: epoxy dlopens libEGL.dylib/libGLESv2.dylib at the
# first EGL call, so dylibbundler's link-time walk never sees them. Ship them
# next to the other dylibs — the bundle's lib/ dir is on the loader path.
ANGLE="$(brew --prefix startergo/angle/angle 2>/dev/null || brew --prefix angle 2>/dev/null || true)"
if [[ -n "$ANGLE" && -f "$ANGLE/lib/libEGL.dylib" ]]; then
  cp "$ANGLE/lib/libEGL.dylib" "$ANGLE/lib/libGLESv2.dylib" "$BUNDLE/lib/"
fi

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
ENTITLEMENTS="$REPO_ROOT/internal/qemubin/qemu-entitlements.plist"
# Sign dylibs first, then the main binary last (so its signature stays valid).
find "$BUNDLE/lib" -name '*.dylib' -exec codesign --force --sign - --timestamp=none {} \;
codesign --force --sign - --entitlements "$ENTITLEMENTS" --timestamp=none \
  "$BUNDLE/bin/qemu-system-aarch64"
codesign --verify --verbose "$BUNDLE/bin/qemu-system-aarch64"

echo "==> Writing GPLv2 compliance files (COPYING + SOURCE.txt)"
# QEMU is GPLv2-only; publishing this bundle distributes QEMU binaries, so ship
# the license text and a corresponding-source pointer. This build links QEMU's
# GL stack against the startergo tap's virglrenderer 1.x + ANGLE (Metal), and
# carries exactly one source modification (the egl-helpers.c cast above) —
# recorded in QEMU_PATCHES so SOURCE.txt discloses it, and published as a
# patch-file asset (see PATCH_FILE above) on the release page.
QEMU_PATCHES="one-line cast in ui/egl-helpers.c (EGLNativeDisplayType -> void* through uintptr_t, for ANGLE's int-typed native display on macOS), published as qemu-${QEMU_VERSION}-macos-egl-helpers.patch on the release page; links against virglrenderer 1.x + ANGLE (GLES->Metal) from the startergo Homebrew taps for accelerated virtio-gpu on macOS" \
RELEASE_TAG="qemu-${QEMU_VERSION}-${VEE_SUFFIX}" \
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
