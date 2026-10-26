#!/bin/bash
# Offline Wiki - set this Mac to update the drive automatically (weekly, when plugged in).
cd "$(dirname "$0")" || exit 1
case "$(uname -m)" in arm64) BIN=app/offline-wiki-darwin-arm64 ;; *) BIN=app/offline-wiki-darwin-amd64 ;; esac
"./$BIN" install-schedule
echo; read -n 1 -s -r -p "Press any key to close."
