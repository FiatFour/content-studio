#!/bin/sh
# Stub nvidia-smi: prints the number in $FAKE_VRAM_FILE; exits 9 if FAKE_SMI_FAIL is set.
[ -n "${FAKE_SMI_FAIL:-}" ] && exit 9
cat "$FAKE_VRAM_FILE"
echo
