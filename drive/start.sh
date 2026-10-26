#!/bin/sh
# Offline Wiki - start on Linux:  sh start.sh   (add --lan to share on the network)
cd "$(dirname "$0")" || exit 1
case "$(uname -m)" in aarch64|arm64) BIN=app/offline-wiki-linux-arm64 ;; *) BIN=app/offline-wiki-linux-amd64 ;; esac
# Drives formatted exFAT may be mounted without execute permission; copy to /tmp if so.
if [ ! -x "$BIN" ]; then
  T=$(mktemp -d); cp "$BIN" "$T/ow"; chmod +x "$T/ow"; exec "$T/ow" --root "$(pwd)" run "$@"
fi
exec "./$BIN" run "$@"
