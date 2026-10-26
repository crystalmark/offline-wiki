#!/bin/bash
# Offline Wiki - double-click to start on a Mac.
# The drive is switched to read-only while Offline Wiki runs, so it can be
# unplugged at any time. To do that, nothing may run from the drive itself,
# so this script and the program run as copies on this Mac.
CACHE="$HOME/Library/Caches/OfflineWiki"; mkdir -p "$CACHE"
if [ -z "$OW_DRIVE" ]; then
  export OW_DRIVE="$(cd "$(dirname "$0")" && pwd)"
  cp "$0" "$CACHE/start.command" && cd / && exec /bin/bash "$CACHE/start.command" "$@"
fi
case "$(uname -m)" in arm64) P=darwin-arm64 ;; *) P=darwin-amd64 ;; esac
B="$CACHE/offline-wiki"
cp "$OW_DRIVE/app/offline-wiki-$P" "$B.new" && mv -f "$B.new" "$B" || { echo "Couldn't copy the program from the drive."; exit 1; }
xattr -c "$B" 2>/dev/null; chmod +x "$B"
exec "$B" --root "$OW_DRIVE" run "$@"
