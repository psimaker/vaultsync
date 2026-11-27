package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"
)

// #224: a computer that runs `vaultsync uninstall --remove-data` and is set up
// again pairs with a new device identity under the same name, and the Hub
// keeps the old one for good. `status` lists both; it has to tell them apart.

const fakeOtherDeviceID = "CCCCCCC-CCCCCCC-CCCCCCC-CCCCCCC-CCCCCCC-CCCCCCC-CCCCCCC-CCCCCCC"

// captureStdout runs fn with os.Stdout sent into a pipe and returns what it
// printed there.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	runErr := fn()
	os.Stdout = saved
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out, runErr
}

// statusConfig points the Hub's commands at a Syncthing API: SYNCTHING_CONFIG
// names a config.xml that carries only the key, and the URL override names
// the API itself.
func statusConfig(t *testing.T, apiURL, apiKey, statePath string) config {
	t.Helper()
	dir := t.TempDir()
	xmlPath := filepath.Join(dir, "config.xml")
	if err := writeFile(xmlPath, `<configuration><gui tls="false"><address>127.0.0.1:8384</address><apikey>`+apiKey+`</apikey></gui></configuration>`); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SYNCTHING_CONFIG", xmlPath)
	if statePath == "" {
		statePath = filepath.Join(dir, "state.json")
	}
	return config{
		apiURL:      apiURL,
		statePath:   statePath,
		vaultsRoot:  "/var/syncthing/vaults",
		vaultsLocal: filepath.Join(dir, "vaults"),
		port:        defaultPort,
		hubName:     defaultHubName,
	}
}

// deviceRows returns the rows of the Devices section of status that hold name.
func deviceRows(out, name string) []string {
	var rows []string
	inDevices := false
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "Devices (") {
			inDevices = true
			continue
		}
		if inDevices && strings.Contains(line, name) {
			rows = append(rows, line)
		}
	}
	return rows
}

// testDeviceID is a device ID whose groups all read like group, so its short
// ID is group itself.
func testDeviceID(group string) string {
	return strings.TrimSuffix(strings.Repeat(group+"-", 8), "-")
}

// runStatus runs `status` against the fake and returns what it printed.
func runStatus(t *testing.T, fake *fakeSyncthing, srvURL string) string {
	t.Helper()
	cfg := statusConfig(t, srvURL, fake.apiKey, "")
	out, err := captureStdout(t, func() error { return cmdStatus(context.Background(), cfg) })
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	return out
}

// deviceSection returns the rows below "Devices (".
func deviceSection(out string) []string {
	_, after, ok := strings.Cut(out, "Devices (")
	if !ok {
		return nil
	}
	lines := strings.Split(strings.TrimRight(after, "\n"), "\n")
	return lines[1:]
}

func TestIssue224_StatusTellsTwoDevicesOfOneNameApart(t *testing.T) {
	fake, srv := newFakeSyncthing(t, fakeHubID)
	fake.devices = append(fake.devices,
		deviceConfig{DeviceID: fakeDeviceID, Name: "hermes"},
		deviceConfig{DeviceID: fakeOtherDeviceID, Name: "hermes"},
	)
	fake.connected[fakeOtherDeviceID] = true
	seen := time.Date(2026, 10, 7, 14, 2, 0, 0, time.UTC)
	fake.lastSeen[fakeDeviceID] = seen
	fake.lastSeen[fakeOtherDeviceID] = time.Now()
	fake.folders = []folderConfig{{
		ID: "vs-000000000001", Label: "Notes", Path: "/var/syncthing/vaults/notes",
		Devices: []folderDevice{{DeviceID: fakeHubID}, {DeviceID: fakeDeviceID}, {DeviceID: fakeOtherDeviceID}},
	}}

	out := runStatus(t, fake, srv.URL)
	rows := deviceRows(out, "hermes")
	if len(rows) != 2 {
		t.Fatalf("status lists %d rows for hermes, want 2:\n%s", len(rows), out)
	}
	// The first group of a device ID is what Syncthing shows as its short ID;
	// it begins the row and tells one "hermes" from the other.
	want := []string{
		fmt.Sprintf("  BBBBBBB  %-32s offline, last seen %s", "hermes", seen.Local().Format("2006-01-02 15:04 MST")),
		fmt.Sprintf("  CCCCCCC  %-32s connected", "hermes"),
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("row %d:\n got %q\nwant %q\n%s", i, rows[i], want[i], out)
		}
	}
}

func TestIssue224_StatusSaysWhenAnOfflineDeviceWasLastSeen(t *testing.T) {
	fake, srv := newFakeSyncthing(t, fakeHubID)
	seen := time.Date(2026, 11, 2, 9, 30, 0, 0, time.UTC)
	ids := map[string]string{
		"connected": testDeviceID("CCCCCCC"),
		"seen":      testDeviceID("DDDDDDD"),
		"never":     testDeviceID("EEEEEEE"),
		"unlisted":  testDeviceID("FFFFFFF"),
	}
	for _, name := range []string{"connected", "seen", "never", "unlisted"} {
		fake.devices = append(fake.devices, deviceConfig{DeviceID: ids[name], Name: name})
	}
	fake.connected[ids["connected"]] = true
	fake.lastSeen[ids["connected"]] = seen
	fake.lastSeen[ids["seen"]] = seen
	// What Syncthing reports for a device it has never seen.
	fake.lastSeen[ids["never"]] = time.Unix(0, 0)

	rows := deviceSection(runStatus(t, fake, srv.URL))
	want := []string{
		fmt.Sprintf("  CCCCCCC  %-32s connected", "connected"),
		fmt.Sprintf("  DDDDDDD  %-32s offline, last seen %s", "seen", seen.Local().Format("2006-01-02 15:04 MST")),
		fmt.Sprintf("  EEEEEEE  %-32s offline, never seen", "never"),
		fmt.Sprintf("  FFFFFFF  %-32s offline", "unlisted"),
	}
	if strings.Join(rows, "\n") != strings.Join(want, "\n") {
		t.Fatalf("rows:\n%s\nwant:\n%s", strings.Join(rows, "\n"), strings.Join(want, "\n"))
	}
}

// setup.sh waits until status answers and then prints its second line, the
// Hub's device ID: neither may hang on the device statistics.
func TestIssue224_StatusAnswersWithoutTheDeviceStatistics(t *testing.T) {
	fake, srv := newFakeSyncthing(t, fakeHubID)
	fake.devices = append(fake.devices, deviceConfig{DeviceID: fakeDeviceID, Name: "hermes"})
	fake.lastSeen[fakeDeviceID] = time.Now()
	fake.statsFail = true

	out := runStatus(t, fake, srv.URL)
	if lines := strings.Split(out, "\n"); len(lines) < 2 || lines[1] != "Device ID:   "+fakeHubID {
		t.Errorf("the second line must be the Hub's device ID, for setup.sh:\n%s", out)
	}
	if rows := deviceSection(out); len(rows) != 1 || rows[0] != fmt.Sprintf("  BBBBBBB  %-32s offline", "hermes") {
		t.Errorf("rows without statistics: %q", rows)
	}
}

// A device's name is set by the device, and in Syncthing's Web UI by anyone
// who has it: in the row it is cleaned like every name the Hub keeps, so it
// can neither break the row nor move the short ID that begins it (061).
func TestIssue224_ANameCanNeitherBreakNorMoveItsRow(t *testing.T) {
	fake, srv := newFakeSyncthing(t, fakeHubID)
	names := []struct{ group, name, shown string }{
		{"DDDDDDD", "two\u2028lines", "twolines"},
		{"EEEEEEE", "\u202Eesrever", "esrever"},
		{"FFFFFFF", "car\rriage", "carriage"},
		{"GGGGGGG", "csi\u009b2J", "csi2J"},
		{"HHHHHHH", "\u05e9\u05dc\u05d5\u05dd 12", "\u05e9\u05dc\u05d5\u05dd 12"},
		{"IIIIIII", "\u200b\u200e", "(unnamed)"},
	}
	for _, n := range names {
		fake.devices = append(fake.devices, deviceConfig{DeviceID: testDeviceID(n.group), Name: n.name})
	}

	out := runStatus(t, fake, srv.URL)
	rows := deviceSection(out)
	if len(rows) != len(names) {
		t.Fatalf("%d rows for %d devices:\n%s", len(rows), len(names), out)
	}
	for i, n := range names {
		if want := fmt.Sprintf("  %s  %-32s offline", n.group, n.shown); rows[i] != want {
			t.Errorf("row %d:\n got %q\nwant %q", i, rows[i], want)
		}
	}
	for _, r := range out {
		hidden := r != '\n' && unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp)
		if hidden {
			t.Errorf("status printed U+%04X", r)
		}
	}
}

// A stalled statistics read must not hold up status: the setup link runs it
// in a loop until it answers.
func TestIssue224_StatusDoesNotWaitLongForTheDeviceStatistics(t *testing.T) {
	saved := lastSeenWait
	lastSeenWait = 200 * time.Millisecond
	t.Cleanup(func() { lastSeenWait = saved })
	fake, srv := newFakeSyncthing(t, fakeHubID)
	fake.devices = append(fake.devices, deviceConfig{DeviceID: fakeDeviceID, Name: "hermes"})
	fake.lastSeen[fakeDeviceID] = time.Now()
	fake.statsDelay = 20 * time.Second

	start := time.Now()
	out := runStatus(t, fake, srv.URL)
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("status took %s with the statistics stalled", took)
	}
	if rows := deviceSection(out); len(rows) != 1 || rows[0] != fmt.Sprintf("  BBBBBBB  %-32s offline", "hermes") {
		t.Errorf("rows with the statistics stalled: %q", rows)
	}
	if lines := strings.Split(out, "\n"); len(lines) < 2 || lines[1] != "Device ID:   "+fakeHubID {
		t.Errorf("the second line must be the Hub's device ID, for setup.sh:\n%s", out)
	}
}

// A vault's name is cleaned like the device names beside it (decision 061):
// a device that creates a vault names it.
func TestIssue224_AVaultsNameIsCleanedLikeADevicesName(t *testing.T) {
	fake, srv := newFakeSyncthing(t, fakeHubID)
	names := []struct{ id, label, shown string }{
		{"vs-000000000001", "two\u2028lines", "twolines"},
		{"vs-000000000002", "\u202Eesrever", "esrever"},
		{"vs-000000000003", "clear\x1b[2J", "clear[2J"},
		{"vs-000000000004", "\u200b", "vs-000000000004"},
	}
	for _, n := range names {
		fake.folders = append(fake.folders, folderConfig{ID: n.id, Label: n.label, Path: "/var/syncthing/vaults/" + n.id, Devices: []folderDevice{{DeviceID: fakeHubID}}})
	}
	out := runStatus(t, fake, srv.URL)
	_, after, _ := strings.Cut(out, "Vaults (")
	rows := strings.Split(after, "\n")[1 : 1+len(names)]
	for i, n := range names {
		if want := fmt.Sprintf("  %-32s %14s   shared with 0 device(s)", n.shown, "unknown"); rows[i] != want {
			t.Errorf("vault row %d:\n got %q\nwant %q", i, rows[i], want)
		}
	}
	for _, r := range out {
		if r != '\n' && unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp) {
			t.Errorf("status printed U+%04X", r)
		}
	}
}
