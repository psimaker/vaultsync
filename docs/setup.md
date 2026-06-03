# Build & development setup

## 🧰 Prerequisites

| Tool | Install |
|---|---|
| macOS + Xcode 26+ | App Store |
| Go 1.26+ | `brew install go` |
| gomobile + gobind | `make setup` (step 2) |
| XcodeGen | `brew install xcodegen` |

## 🔨 Build

```bash
git clone https://github.com/psimaker/vaultsync.git && cd vaultsync

cd go
make setup        # 1. install gomobile + gobind, run gomobile init
make patch        # 2. create _syncthing_patched/ with applied fixes
make xcframework  # 3. build go/build/SyncBridge.xcframework (~160 MB)

cd ../ios
xcodegen generate # 4. generate VaultSync.xcodeproj
open VaultSync.xcodeproj
```

The xcframework targets iOS 18+ (arm64 device) and the Simulator (arm64 + x86_64). The ~160 MB size is expected — it bundles the full Syncthing engine per slice. In Xcode, select the **VaultSync** scheme, pick an iOS 18+ destination, and build (⌘B).

> **Device builds need a signing team.** No team is committed. Copy `ios/Signing.local.xcconfig.example` to `ios/Signing.local.xcconfig`, set your `DEVELOPMENT_TEAM`, and re-run `xcodegen generate`. That file is gitignored and survives regeneration. Simulator builds need no team — pass `CODE_SIGNING_ALLOWED=NO`.

## 🧪 Tests

```bash
cd go && make test     # Go bridge tests

cd ios && xcodebuild test \
  -project VaultSync.xcodeproj \
  -scheme VaultSync \
  -destination 'platform=iOS Simulator,name=iPhone 17 Pro,OS=latest' \
  CODE_SIGNING_ALLOWED=NO -quiet
```

## 🩺 Build troubleshooting

| Problem | Fix |
|---|---|
| `gomobile` not found | Add `$(go env GOPATH)/bin` to `$PATH` |
| xcframework build fails | `make clean`, then retry `make xcframework` |
| Xcode project missing | Run `xcodegen generate` in `ios/` |
| Simulator unavailable | Install an iOS 18+ simulator runtime in Xcode |
| Runtime / sync issues | See [troubleshooting.md](troubleshooting.md) |
