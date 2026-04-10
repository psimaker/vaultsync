# Makefile for building the Go bridge as .xcframework via gomobile
#
# Targets:
#   xcframework  - Build SyncBridge.xcframework for iOS + simulator
#   clean        - Remove build artifacts
#   test         - Run Go tests
#
# Prerequisites:
#   - Go 1.26+ (brew install go)
#   - gomobile (go install golang.org/x/mobile/cmd/gomobile@latest)
#   - gobind (go install golang.org/x/mobile/cmd/gobind@latest)
#   - Xcode (xcode-select -s /Applications/Xcode.app/Contents/Developer)

GOPATH_BIN := $(shell go env GOPATH)/bin
GOMOBILE := $(GOPATH_BIN)/gomobile
OUTPUT := build/SyncBridge.xcframework
MIN_IOS := 26

export PATH := $(GOPATH_BIN):$(PATH)

.PHONY: xcframework clean test setup patch

xcframework:
	@mkdir -p build
	$(GOMOBILE) bind -target=ios,iossimulator -iosversion=$(MIN_IOS) -tags noassets -o $(OUTPUT) ./bridge/
	@echo "Built $(OUTPUT)"
	@du -sh $(OUTPUT)

clean:
	rm -rf build/

test:
	go test -tags noassets ./bridge/...

# Re-create _syncthing_patched from the module cache with all patches applied.
patch:
	./vendor-patch.sh

setup:
	go install golang.org/x/mobile/cmd/gomobile@latest
	go install golang.org/x/mobile/cmd/gobind@latest
	$(GOMOBILE) init
