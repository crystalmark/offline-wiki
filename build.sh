#!/bin/sh
# Builds the drive contents into dist/. Copy dist/* to the root of the drive,
# or run:  ./build.sh /Volumes/OFFLINEWIKI   to build straight onto it.
set -e
cd "$(dirname "$0")"
OUT=${1:-dist}
mkdir -p "$OUT/app"
for t in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64; do
  os=${t%/*}; arch=${t#*/}; ext=""; [ "$os" = windows ] && ext=".exe"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w" -o "$OUT/app/offline-wiki-$os-$arch$ext" .
done
cp drive/* README.txt "$OUT/"
chmod +x "$OUT"/*.command "$OUT"/*.sh "$OUT"/app/*
case "$(uname -s)-$(uname -m)" in
  Darwin-arm64) H=darwin-arm64 ;; Darwin-*) H=darwin-amd64 ;; Linux-aarch64) H=linux-arm64 ;; *) H=linux-amd64 ;;
esac
"$OUT/app/offline-wiki-$H" --root "$OUT" init
