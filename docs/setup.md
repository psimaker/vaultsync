# Setup — Build Instructions

## Prerequisites

- **macOS** with Xcode 26+ installed
- **Go 1.26+** (`brew install go`)
- **gomobile** and **gobind** (installed via `make setup`)
- **XcodeGen** (`brew install xcodegen`)

## Build Steps

### 1. Clone the repository

```bash
git clone https://github.com/psimaker/vaultsync.git
cd vaultsync
```

### 2. Install Go build tools

```bash
cd go
make setup    # installs gomobile + gobind, runs gomobile init
```

### 3. Apply dependency patches

```bash
make patch    # creates _syncthing_patched/ with applied fixes
```

### 4. Build the Go xcframework

```bash
make xcframework
```

This produces `go/build/SyncBridge.xcframework` (~30-50 MB) targeting iOS 18+ (arm64) and iOS Simulator (arm64).

### 5. Generate the Xcode project

```bash
cd ../ios
xcodegen generate
```

### 6. Open and build in Xcode

```bash
open VaultSync.xcodeproj
```

Select the **VaultSync** scheme, choose a supported device or simulator (iOS 18+), and build (Cmd+B).

## Running Tests

```bash
# Go bridge tests
cd go && make test

# iOS unit tests (from Xcode or CLI)
cd ios && xcodebuild test \
  -project VaultSync.xcodeproj \
  -scheme VaultSync \
  -destination 'platform=iOS Simulator,name=iPhone 17 Pro,OS=latest' \
  -quiet
```

## Troubleshooting

- **gomobile not found:** Ensure `$(go env GOPATH)/bin` is in your `$PATH`.
- **xcframework build fails:** Run `make clean` first, then retry `make xcframework`.
- **Xcode project missing:** Run `xcodegen generate` in the `ios/` directory.
- **Simulator not available:** Ensure you have a supported iOS 18+ simulator runtime installed in Xcode.
- **Runtime or sync issues after build:** Use [docs/troubleshooting.md](troubleshooting.md).
