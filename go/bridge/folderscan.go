// Folder scanner: detects known heavy directories in a vault to power
// the "Found in this vault" section of the Sync Filters UI.
package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// DetectedPattern describes one heavy directory found in a vault.
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

// ScanFolderForKnownPatterns walks a vault for known heavy directories
// and returns size + file count per match as JSON.
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

	detected := []DetectedPattern{}
	for _, c := range heavyDirCandidates {
		full := filepath.Join(folder.Path, c.Pattern)
		info, err := os.Stat(full)
		if err != nil || !info.IsDir() {
			continue
		}
		size, count := dirSizeAndCount(full)
		if count == 0 {
			continue
		}
		detected = append(detected, DetectedPattern{
			Pattern:   c.Pattern,
			Label:     c.Label,
			SizeBytes: size,
			FileCount: count,
		})
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
