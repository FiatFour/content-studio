#!/usr/bin/env bash
# One-time (idempotent) setup of a headless Chromium for the verify driver. Needs no sudo:
# Playwright's browser is downloaded into ~/.cache, and the two system libraries it needs
# (libnss3, libnspr4) are unpacked from .deb files into var/verify/tools/libs.
# Playwright 1.63+ is required: 1.55 has no build for Ubuntu 26.04.
set -euo pipefail
cd "$(dirname "$0")/../../../.."
T=var/verify/tools
mkdir -p "$T/debs" "$T/libs"
[ -d "$T/node_modules/playwright" ] || (cd "$T" && npm init -y >/dev/null && npm i playwright@1.63.0 >/dev/null)
(cd "$T" && npx playwright install chromium >/dev/null)
if [ ! -e "$T/libs/usr/lib/x86_64-linux-gnu/libnss3.so" ]; then
  (cd "$T/debs" && apt-get download libnss3 libnspr4 >/dev/null 2>&1 && for f in *.deb; do dpkg-deb -x "$f" ../libs; done)
fi
echo "browser ready; drive.mjs sets LD_LIBRARY_PATH itself"
