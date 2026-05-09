// Folder scanner: detects known heavy directories in a vault to power
// the "Found in this vault" section of the Sync Filters UI.
//
// In typical Obsidian setups the sync folder is the Obsidian root, and the
// actual vaults are immediate subdirectories. The scanner therefore checks
// both the top level and one level deep, aggregating matches per pattern.
package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
// Returns {"detected":[]} for unknown folders, missing paths, or empty vaults.
func ScanFolderForKnownPatterns(folderID string) string {
	folders := getFolderConfigs()
	if folders == nil {
		return `{"detected":[]}`
	}
	folder, ok := folders[folderID]
	if !ok {
		return `{"detected":[]}`
	}

	type accum struct {
		label string
		bytes int64
		count int
	}
	sums := map[string]*accum{}

	checkLocation := func(base string) {
		for _, c := range heavyDirCandidates {
			full := filepath.Join(base, c.Pattern)
			info, err := os.Stat(full)
			if err != nil || !info.IsDir() {
				continue
			}
			size, count := dirSizeAndCount(full)
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
	}

	// Top level (single-vault setups, or stray heavy folders next to the vaults).
	checkLocation(folder.Path)

	// One level deep — the typical "Obsidian root with vault subdirs" layout.
	if entries, err := os.ReadDir(folder.Path); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			checkLocation(filepath.Join(folder.Path, entry.Name()))
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

	data, err := json.Marshal(ScanResult{Detected: detected})
	if err != nil {
		return `{"detected":[]}`
	}
	return string(data)
}

func dirSizeAndCount(root string) (int64, int) {
	var total int64
	var count int
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			total += info.Size()
			count++
		}
		return nil
	})
	return total, count
}
