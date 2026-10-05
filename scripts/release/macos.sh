#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

channel="${1:-}"
if [[ "$channel" != "app-store" && "$channel" != "direct" ]]; then
  echo "Usage: $0 <app-store|direct>" >&2
  exit 2
fi
if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "macOS release builds must run on macOS." >&2
  exit 1
fi

command -v wails >/dev/null || { echo "Wails CLI is required (go install github.com/wailsapp/wails/v2/cmd/wails@v2.16.0)." >&2; exit 1; }
command -v node >/dev/null || { echo "Node.js is required to build the frontend." >&2; exit 1; }

: "${DISKATLAS_BUNDLE_ID:?Set DISKATLAS_BUNDLE_ID to the registered reverse-DNS bundle ID (for example com.example.diskatlas).}"
if [[ ! "$DISKATLAS_BUNDLE_ID" =~ ^[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+$ ]]; then
  echo "DISKATLAS_BUNDLE_ID must be a reverse-DNS identifier." >&2
  exit 2
fi

if [[ "$channel" == "app-store" ]]; then
  : "${MAC_APP_IDENTITY:?Set MAC_APP_IDENTITY to the Apple Distribution signing identity.}"
  : "${MAC_INSTALLER_IDENTITY:?Set MAC_INSTALLER_IDENTITY to the Mac Installer Distribution identity.}"
  if [[ -n "${MAC_PROVISIONING_PROFILE:-}" && ! -f "$MAC_PROVISIONING_PROFILE" ]]; then
    echo "Provisioning profile not found: $MAC_PROVISIONING_PROFILE" >&2
    exit 1
  fi
else
  : "${MAC_APP_IDENTITY:?Set MAC_APP_IDENTITY to the Developer ID Application signing identity.}"
  : "${MAC_NOTARY_PROFILE:?Set MAC_NOTARY_PROFILE to the notarytool Keychain profile name.}"
fi

version="$(node -e 'process.stdout.write(JSON.parse(require("fs").readFileSync("wails.json", "utf8")).info.productVersion)')"
app="$ROOT/build/bin/DiskAtlas.app"
entitlements="$ROOT/build/darwin/entitlements.plist"
output="$ROOT/dist/release/macos/$channel"
mkdir -p "$output"

wails build -platform darwin/universal -clean
[[ -d "$app" ]] || { echo "Wails did not produce $app" >&2; exit 1; }

/usr/libexec/PlistBuddy -c "Set :CFBundleIdentifier $DISKATLAS_BUNDLE_ID" "$app/Contents/Info.plist"
if [[ "$channel" == "app-store" && -n "${MAC_PROVISIONING_PROFILE:-}" ]]; then
  cp "$MAC_PROVISIONING_PROFILE" "$app/Contents/embedded.provisionprofile"
fi

codesign_args=(--force --timestamp --sign "$MAC_APP_IDENTITY")
if [[ "$channel" == "direct" ]]; then
  codesign_args+=(--options runtime)
else
  codesign_args+=(--entitlements "$entitlements")
fi
codesign "${codesign_args[@]}" "$app"
codesign --verify --deep --strict --verbose=2 "$app"

if [[ "$channel" == "app-store" ]]; then
  pkg="$output/DiskAtlas-$version.pkg"
  rm -f "$pkg"
  productbuild --component "$app" /Applications --sign "$MAC_INSTALLER_IDENTITY" "$pkg"
  echo "App Store package created: $pkg"
  exit 0
fi

notary_input="$(mktemp -d "${TMPDIR:-/tmp}/diskatlas-notary.XXXXXX")"
trap 'rm -rf "$notary_input"' EXIT
ditto -c -k --keepParent "$app" "$notary_input/DiskAtlas.zip"
xcrun notarytool submit "$notary_input/DiskAtlas.zip" --keychain-profile "$MAC_NOTARY_PROFILE" --wait
xcrun stapler staple "$app"
xcrun stapler validate "$app"

zip="$output/DiskAtlas-$version-macOS-universal.zip"
dmg="$output/DiskAtlas-$version-macOS-universal.dmg"
rm -f "$zip" "$dmg"
ditto -c -k --keepParent "$app" "$zip"
hdiutil create -volname "DiskAtlas" -srcfolder "$app" -ov -format UDZO "$dmg"
codesign --force --timestamp --identifier "$DISKATLAS_BUNDLE_ID.disk-image" --sign "$MAC_APP_IDENTITY" "$dmg"
codesign --verify --verbose=2 "$dmg"
xcrun notarytool submit "$dmg" --keychain-profile "$MAC_NOTARY_PROFILE" --wait
xcrun stapler staple "$dmg"
xcrun stapler validate "$dmg"
echo "Notarized direct-distribution builds created:"
echo "  $zip"
echo "  $dmg"
