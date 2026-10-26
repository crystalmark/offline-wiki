#!/bin/bash
# Offline Wiki - download or refresh the libraries, models and programs (needs internet).
# You can also use the Update button in the Offline Wiki page.
CACHE="$HOME/Library/Caches/OfflineWiki"; mkdir -p "$CACHE"
if [ -z "$OW_DRIVE" ]; then
  export OW_DRIVE="$(cd "$(dirname "$0")" && pwd)"
  cp "$0" "$CACHE/update.command" && cd / && exec /bin/bash "$CACHE/update.command" "$@"
fi
case "$(uname -m)" in arm64) P=darwin-arm64 ;; *) P=darwin-amd64 ;; esac
B="$CACHE/offline-wiki-update"
cp "$OW_DRIVE/app/offline-wiki-$P" "$B.new" && mv -f "$B.new" "$B" || { echo "Couldn't copy the program from the drive."; exit 1; }
xattr -c "$B" 2>/dev/null; chmod +x "$B"
caffeinate -i "$B" --root "$OW_DRIVE" update "$@"
echo; read -n 1 -s -r -p "Press any key to close."
