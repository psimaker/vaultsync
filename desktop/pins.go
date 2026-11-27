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
const syncthingVersion = "v2.1.7"

// keepEngineFrom is the oldest Syncthing a run keeps running when this pin
// cannot be installed (#267, decision 078): one whose config and REST API
// this agent can drive — the Desktop Syncthing E2E with that version's
// archive under this agent passed. A bump that changes the engine's config
// shape or the API the agent uses raises it to the new pin, which keeps no
// older engine at all.
const keepEngineFrom = "v2.1.6"

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
		archive: "syncthing-macos-amd64-v2.1.7.zip",
		sha256:  "b8be90176d0f24daebae7b5aaddac52ebfae395df6d99f5e0e54656db00868cb",
		binary:  "syncthing-macos-amd64-v2.1.7/syncthing",
	},
	"darwin/arm64": {
		archive: "syncthing-macos-arm64-v2.1.7.zip",
		sha256:  "2df075b463231a307c91da657157beaed06383a941ba7ea04d33d536e3c4af38",
		binary:  "syncthing-macos-arm64-v2.1.7/syncthing",
	},
	"linux/amd64": {
		archive: "syncthing-linux-amd64-v2.1.7.tar.gz",
		sha256:  "2edc416e7ed136b7c858ed5f3f02a3b8316589605e54c380228ceea7acbf0142",
		binary:  "syncthing-linux-amd64-v2.1.7/syncthing",
	},
	"linux/arm64": {
		archive: "syncthing-linux-arm64-v2.1.7.tar.gz",
		sha256:  "48e456690cd2b97e7d26b02b5ac35f62a3b36172267b144cfeaf02c993ab955c",
		binary:  "syncthing-linux-arm64-v2.1.7/syncthing",
	},
}
