# Release builds

Build each desktop release on its target OS. The scripts use the installed Wails CLI, Go, and Node.js. Windows Store packaging also needs the Windows SDK (`MakeAppx.exe`); Windows direct distribution needs NSIS (`makensis`) and, for a public release, an Authenticode certificate available to SignTool.

Artifacts are written under the ignored `dist/release/` directory.

## macOS

Choose a stable bundle ID registered to the Apple developer account, then set `DISKATLAS_BUNDLE_ID` for both channels. The script builds `DiskAtlas.app`, sets that bundle ID, signs it with the channel's identity, and verifies the signature.

### Mac App Store

Requires an **Apple Distribution** identity for the app and a **Mac Installer Distribution** identity for the `.pkg`. Register the bundle ID in the Apple Developer account and create the app record in App Store Connect. A provisioning profile is optional for the current entitlements; if you add restricted Apple services later, set `MAC_PROVISIONING_PROFILE` to the matching distribution profile.

```sh
export DISKATLAS_BUNDLE_ID=com.example.diskatlas
export MAC_APP_IDENTITY='Apple Distribution: Example, Inc. (TEAMID)'
export MAC_INSTALLER_IDENTITY='3rd Party Mac Developer Installer: Example, Inc. (TEAMID)'
npm run release:mac:store
```

Output: `dist/release/macos/app-store/DiskAtlas-<version>.pkg`. Upload that package through Transporter or App Store Connect. The App Store package is sandboxed and carries selected-folder read/write and bookmark entitlements in `build/darwin/entitlements.plist`; users grant access by choosing folders in the app. This channel cannot have unrestricted access to `/` by default, so it cannot promise the same whole-disk coverage as the direct build. Protected locations can still be inaccessible even after a folder is chosen.

### Direct download

Requires a **Developer ID Application** identity and a `notarytool` Keychain profile. Create the profile once; keep credentials in Keychain rather than in shell history or this repository:

```sh
xcrun notarytool store-credentials diskatlas-notary --apple-id 'you@example.com' --team-id TEAMID --password 'app-specific-password'
export DISKATLAS_BUNDLE_ID=com.example.diskatlas
export MAC_APP_IDENTITY='Developer ID Application: Example, Inc. (TEAMID)'
export MAC_NOTARY_PROFILE=diskatlas-notary
npm run release:mac:direct
```

Output: a stapled universal `.dmg` and `.zip` in `dist/release/macos/direct/`. The app and disk image are signed and notarized; their tickets are stapled before release. Direct builds are not App Sandbox builds, so normal macOS privacy controls (including Full Disk Access where required) still apply. Use this channel when whole-disk exploration is the priority.

## Windows

Run PowerShell on Windows. Both paths support `x64` (default) and `arm64` via `-Architecture`.

### Microsoft Store

Copy the package identity **Name** and **Publisher** exactly from Partner Center. The script creates both an unsigned `.msix` and an `.msixupload` container; Microsoft signs the app package after Store submission.

```powershell
npm run release:windows:store -- -IdentityName 'YourStoreIdentity' -Publisher 'CN=Publisher from Partner Center' -PublisherDisplayName 'Your publisher name'
```

For ARM64, append `-Architecture arm64`. The manifest requests `runFullTrust`, which the Store treats as a restricted capability and must approve. This is required for the Wails desktop app to access the filesystem. The package build itself does not grant broader filesystem access than the app's normal Windows process permissions.

### Direct download

Use an Authenticode certificate whose private key is installed in the Windows certificate store. Pass its SHA-1 thumbprint; SignTool signs and verifies both the app executable and the rebuilt NSIS installer.

```powershell
npm run release:windows:direct -- -SignThumbprint 'CERTIFICATE_SHA1_THUMBPRINT'
```

For internal smoke builds only, omit public signing explicitly with `-Unsigned`. ARM64 can be selected with `-Architecture arm64`.

## Credentials and release identity

The scripts never generate or embed certificates, Apple credentials, Store identity, or signing secrets. Configure these from your developer accounts and Windows certificate store. Keep the bundle ID and Store identity stable between releases; changing either creates a different app identity.
