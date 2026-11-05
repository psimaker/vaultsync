package main

import (
	"strconv"
	"strings"
)

// The one stock Syncthing release this agent installs (decision 046). The
// checksums are part of this binary, never fetched next to the download: an
// archive that does not match is refused before anything is unpacked.
//
// Bumping the pin is a reviewed change: the new version, the four archive
// checksums from the release's signed sha256sum.txt.asc cross-checked against
// GitHub's asset digests, and a green Desktop Syncthing E2E on the new
// archive (the engine's generated config.xml shape is pinned with it).
const syncthingVersion = "v2.1.6"

// engineNewer: Syncthing version a (vX.Y.Z — a suffix such as -rc.1 aside)
// is newer than b. A version that does not read as one is not newer.
func engineNewer(a, b string) bool {
	va, ok := engineVersion(a)
	if !ok {
		return false
	}
	vb, ok := engineVersion(b)
	if !ok {
		return false
	}
	for i := range va {
		if va[i] != vb[i] {
			return va[i] > vb[i]
		}
	}
	return false
}

// recordedEngineNewer: the Syncthing agent.json records is newer than this
// build's pin — or reads as no version this build knows: either way this
// build may be the older one, and installs nothing over it (decision 054).
// Nothing recorded is a first setup.
func recordedEngineNewer(recorded string) bool {
	if recorded == "" {
		return false
	}
	if _, ok := engineVersion(recorded); !ok {
		return true
	}
	return engineNewer(recorded, syncthingVersion)
}

func engineVersion(v string) ([3]int, bool) {
	var out [3]int
	core, _, _ := strings.Cut(strings.TrimPrefix(v, "v"), "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// syncthingDownloadBase is where the pinned archives come from; a test
// points it at a local server (the checksum still decides).
var syncthingDownloadBase = "https://github.com/syncthing/syncthing/releases/download/" + syncthingVersion + "/"

type syncthingPin struct {
	archive string // release asset name
	sha256  string // of the archive, lower-case hex
	binary  string // the syncthing executable inside the archive
}

var syncthingPins = map[string]syncthingPin{
	"darwin/amd64": {
		archive: "syncthing-macos-amd64-v2.1.6.zip",
		sha256:  "d1464be24a7157440dfb52e2d84939f83caf89c129761945abcdd4956e09ffb1",
		binary:  "syncthing-macos-amd64-v2.1.6/syncthing",
	},
	"darwin/arm64": {
		archive: "syncthing-macos-arm64-v2.1.6.zip",
		sha256:  "f86bfa0bdbb5b1b13229a7e5643e898e3c4e017a4fff6dd525994ebaf28ff6e2",
		binary:  "syncthing-macos-arm64-v2.1.6/syncthing",
	},
	"linux/amd64": {
		archive: "syncthing-linux-amd64-v2.1.6.tar.gz",
		sha256:  "524ef4e1df1850b719e2378c8369956f4e5f2ab9a46fdf7f45f2b925012147aa",
		binary:  "syncthing-linux-amd64-v2.1.6/syncthing",
	},
	"linux/arm64": {
		archive: "syncthing-linux-arm64-v2.1.6.tar.gz",
		sha256:  "f054de02462d5d58bd126908f2f92b63aba74f162a5be85e50e96d656e7db47d",
		binary:  "syncthing-linux-arm64-v2.1.6/syncthing",
	},
}
