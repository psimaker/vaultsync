// Folder scanner: detects known heavy directories in a vault to power
// the "Found in this vault" section of the Sync Filters UI.
//
// In typical Obsidian setups the sync folder is the Obsidian root, and the
// actual vaults are immediate subdirectories. The scanner therefore checks
// both the top level and one level deep, aggregating matches per pattern.
package bridge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/syncthing/syncthing/lib/config"
)

// DetectedPattern describes one heavy directory (or aggregate of multiple
// matches) found in a vault.
type DetectedPattern struct {
	Pattern   string `json:"pattern"`
	Label     string `json:"label"`
	SizeBytes int64  `json:"sizeBytes"`
	FileCount int    `json:"fileCount"`
}

// ScanResult is the JSON envelope returned by ScanFolderForKnownPatterns.
type ScanResult struct {
	Detected []DetectedPattern `json:"detected"`
	Complete bool              `json:"complete"`
	Error    string            `json:"error,omitempty"`
}

// The scanner never returns filesystem, folder, or driver details.
const knownPatternScanUnavailableError = "vaultsync-filter-scan-unavailable"

type knownPatternScanEnvironment struct {
	folderConfigs func() map[string]config.FolderConfiguration
	inspectFolder func(string) ([]DetectedPattern, error)
}

var heavyDirCandidates = []struct {
	Pattern string
	Label   string
}{
	{".git", "Git repository"},
	{".copilot-index", "Copilot index"},
	{"node_modules", "Node modules"},
	{".obsidian/cache", "Obsidian app cache"},
}

// ScanFolderForKnownPatterns walks a vault for known heavy directories and
// returns aggregated size + file count per pattern as JSON.
//
// Search depth: the folder root, plus each non-hidden top-level subdirectory
// (the "vault subdir" pattern). Matches in multiple locations are summed
// into a single entry per pattern (e.g. ".git in 3 vaults — 127 MB total").
//
// The running-engine check and SendOnly folder/path snapshot complete before
// the first filesystem access. The synchronous filesystem work then runs
// without holding the lifecycle lock; an already-started walk is not
// cancellable, while a later scan must obtain fresh authorization.
func ScanFolderForKnownPatterns(folderID string) string {
	result := scanFolderForKnownPatterns(folderID, knownPatternScanEnvironment{
		folderConfigs: getFolderConfigs,
		inspectFolder: inspectKnownPatterns,
	})
	return marshalKnownPatternScanResult(result)
}

func unavailableKnownPatternScanResult() ScanResult {
	return ScanResult{
		Detected: []DetectedPattern{},
		Complete: false,
		Error:    knownPatternScanUnavailableError,
	}
}

func scanFolderForKnownPatterns(folderID string, env knownPatternScanEnvironment) ScanResult {
	if env.folderConfigs == nil || env.inspectFolder == nil {
		return unavailableKnownPatternScanResult()
	}
	folders := env.folderConfigs()
	if folders == nil {
		return unavailableKnownPatternScanResult()
	}
	folder, ok := folders[folderID]
	if !ok || folder.Type != config.FolderTypeSendOnly {
		return unavailableKnownPatternScanResult()
	}
	detected, err := env.inspectFolder(folder.Path)
	if err != nil {
		return unavailableKnownPatternScanResult()
	}
	if detected == nil {
		detected = []DetectedPattern{}
	}
	return ScanResult{Detected: detected, Complete: true}
}

func inspectKnownPatterns(root string) ([]DetectedPattern, error) {
	rootInfo, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !rootInfo.IsDir() {
		return nil, fmt.Errorf("scan root is not a directory")
	}

	type accum struct {
		label string
		bytes int64
		count int
	}
	sums := map[string]*accum{}

	checkLocation := func(base string) error {
		for _, c := range heavyDirCandidates {
			full := filepath.Join(base, c.Pattern)
			info, err := os.Stat(full)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			if !info.IsDir() {
				continue
			}
			size, count, err := dirSizeAndCount(full)
			if err != nil {
				return err
			}
			if count == 0 {
				continue
			}
			if a, exists := sums[c.Pattern]; exists {
				a.bytes += size
				a.count += count
			} else {
				sums[c.Pattern] = &accum{label: c.Label, bytes: size, count: count}
			}
		}
		return nil
	}

	// Top level (single-vault setups, or stray heavy folders next to the vaults).
	if err := checkLocation(root); err != nil {
		return nil, err
	}

	// One level deep — the typical "Obsidian root with vault subdirs" layout.
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if err := checkLocation(filepath.Join(root, entry.Name())); err != nil {
			return nil, err
		}
	}

	// Emit in the order of heavyDirCandidates for stable output.
	detected := []DetectedPattern{}
	for _, c := range heavyDirCandidates {
		if a, exists := sums[c.Pattern]; exists {
			detected = append(detected, DetectedPattern{
				Pattern:   c.Pattern,
				Label:     a.label,
				SizeBytes: a.bytes,
				FileCount: a.count,
			})
		}
	}

	return detected, nil
}

func marshalKnownPatternScanResult(result ScanResult) string {
	data, err := json.Marshal(result)
	if err != nil {
		data, _ = json.Marshal(unavailableKnownPatternScanResult())
	}
	return string(data)
}

func dirSizeAndCount(root string) (int64, int, error) {
	var total int64
	var count int
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			total += info.Size()
			count++
		}
		return nil
	})
	return total, count, err
}
