package bridge

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// ReadFileContent used to read a conflict note of any size into memory and
// hand it across the bridge, where the app rendered and diffed it on the
// spot — a large note froze the UI or got the app killed by the watchdog
// (#184). Reads are bounded: at the cap the content comes back, one byte
// over is refused with a marker that names the size, and nothing is read.
func TestIssue184_ReadFileContentBounded(t *testing.T) {
	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	folderPath := filepath.Join(configDir, "readbound")
	if errMsg := AddFolder("readbound", "Read Bound", folderPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}

	limit := MaxReadFileBytes()
	if limit <= 0 {
		t.Fatalf("MaxReadFileBytes() = %d, want a positive bound", limit)
	}

	atCap := bytes.Repeat([]byte("a"), int(limit))
	if err := os.WriteFile(filepath.Join(folderPath, "at-cap.md"), atCap, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ReadFileContent("readbound", "at-cap.md"); int64(len(got)) != limit || strings.HasPrefix(got, "error:") {
		t.Fatalf("ReadFileContent(at cap) returned %d bytes (prefix %q), want the %d-byte content", len(got), head(got), limit)
	}

	overCap := append(atCap, 'b')
	if err := os.WriteFile(filepath.Join(folderPath, "over-cap.md"), overCap, 0o600); err != nil {
		t.Fatal(err)
	}
	got := ReadFileContent("readbound", "over-cap.md")
	if !strings.HasPrefix(got, "error:too large:") {
		t.Fatalf("ReadFileContent(over cap) returned %d bytes (prefix %q), want an error:too large: refusal", len(got), head(got))
	}
	if size := strings.TrimPrefix(got, "error:too large:"); size != strconv.Itoa(len(overCap)) {
		t.Fatalf("too-large marker names size %q, want %d", size, len(overCap))
	}
}

func head(s string) string {
	if len(s) > 24 {
		return s[:24]
	}
	return s
}
