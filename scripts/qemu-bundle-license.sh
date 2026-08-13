#!/usr/bin/env bash
#
# qemu-bundle-license.sh — write the GPLv2 compliance files into a vee-qemu
# bundle directory before it is packaged.
#
# QEMU's system emulator is licensed GPLv2-only. Publishing a vee-qemu bundle
# distributes QEMU binaries, so under GPLv2 §1/§3 the distribution MUST carry
# the license text and the complete corresponding source. vee satisfies §3 by
# attaching the exact upstream source tarball (plus any patch files) to the
# same GitHub release as the binary bundles — equivalent access from the same
# place. This helper ships the license text and a SOURCE.txt pointing at those
# release assets, so every published bundle (linux, macOS, windows) is
# compliant in the same way.
#
# It copies the license text straight out of the extracted QEMU source tree
# (QEMU ships its GPLv2 as COPYING at the tree root) — the most authoritative
# copy — and writes a SOURCE.txt recording exactly how to obtain and rebuild
# the corresponding source.
#
# Usage:
#   qemu-bundle-license.sh <bundle-dir> <qemu-src-dir> <qemu-version> <configure-flags...>
#
# <bundle-dir>   the assembled bundle root (containing bin/, lib/, share/)
# <qemu-src-dir> the extracted qemu-<version> source tree (has COPYING)
# <qemu-version> upstream QEMU version, e.g. 10.0.2
# remaining args the exact ./configure flags used, recorded verbatim
#
# Env:
#   QEMU_PATCHES  optional. If set, describes patches applied on top of the
#                 upstream tarball (e.g. a virglrenderer/ANGLE tap). When unset
#                 the build is treated as an unmodified upstream build.
#   RELEASE_TAG   optional. The vee release tag these bundles are published
#                 under (e.g. qemu-10.0.2-vee1). SOURCE.txt then links the
#                 exact release page carrying the source assets; when unset it
#                 links the releases index.
set -euo pipefail

BUNDLE_DIR="${1:?bundle dir}"
SRC_DIR="${2:?qemu source dir}"
QEMU_VERSION="${3:?qemu version}"
shift 3
CONFIGURE_FLAGS="$*"

LIC_DIR="$BUNDLE_DIR/share/licenses/qemu"
mkdir -p "$LIC_DIR"

# GPLv2 text (and QEMU's own per-component LICENSE notes) from the source tree.
if [[ -f "$SRC_DIR/COPYING" ]]; then
  cp "$SRC_DIR/COPYING" "$LIC_DIR/COPYING"
else
  echo "error: $SRC_DIR/COPYING not found — cannot ship GPLv2 text" >&2
  exit 1
fi
# QEMU's LICENSE file summarizes the per-subsystem licenses (GPLv2, LGPLv2.1,
# BSD, …). Ship it too when present; it is informative, not required.
[[ -f "$SRC_DIR/LICENSE" ]] && cp "$SRC_DIR/LICENSE" "$LIC_DIR/LICENSE"

TARBALL_URL="https://download.qemu.org/qemu-${QEMU_VERSION}.tar.xz"

if [[ -n "${QEMU_PATCHES:-}" ]]; then
  PATCH_PARAGRAPH="This build applies patches on top of the upstream QEMU sources:
    ${QEMU_PATCHES}
  The complete corresponding source is the upstream tarball above plus those
  patches; both are attached to the vee release page referenced below and
  reachable from the vee build scripts."
else
  PATCH_PARAGRAPH="This build applies no patches to the upstream QEMU sources; the tarball above IS
the corresponding source."
fi

if [[ -n "${RELEASE_TAG:-}" ]]; then
  RELEASE_URL="https://github.com/Benehiko/vee/releases/tag/${RELEASE_TAG}"
else
  RELEASE_URL="https://github.com/Benehiko/vee/releases"
fi

cat > "$LIC_DIR/SOURCE.txt" <<EOF
Corresponding source for this QEMU build
=========================================

The qemu-system binary in this bundle is built from upstream QEMU
${QEMU_VERSION}. QEMU is licensed under the GNU General Public License, version 2
(GPLv2-only); see COPYING in this directory for the full text.

Under GPLv2 you are entitled to the complete corresponding source code for this
binary. It is:

  Upstream source tarball:
    ${TARBALL_URL}

  Exact build recipe (configure flags used for this binary):
    ./configure ${CONFIGURE_FLAGS}

  Exact vee build scripts / CI config that produced this bundle:
    vee-build-scripts.tar.gz — snapshot attached to the release page below
    (living copies: https://github.com/Benehiko/vee/tree/main/scripts)

${PATCH_PARAGRAPH}

(Bundled non-QEMU libraries — e.g. virglrenderer, ANGLE, MoltenVK on macOS, or
the MinGW runtime on Windows — are separately licensed under permissive/LGPL
terms; their licenses are shipped alongside this file.)

The complete corresponding source is distributed from the same place as this
bundle (GPLv2 section 3): the exact upstream tarball qemu-${QEMU_VERSION}.tar.xz
(and any patch files noted above) are attached as assets to the release page
that published this bundle:

  ${RELEASE_URL}
EOF

echo "==> Wrote GPLv2 compliance files to $LIC_DIR"
ls -1 "$LIC_DIR"
