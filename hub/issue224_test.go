package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestIssue224_StatusTellsTwoDevicesOfOneNameApart(t *testing.T) {
	fake, srv := newFakeSyncthing(t, fakeHubID)
	fake.devices = append(fake.devices,
		deviceConfig{DeviceID: fakeDeviceID, Name: "hermes"},
		deviceConfig{DeviceID: fakeOtherDeviceID, Name: "hermes"},
	)
	fake.connected[fakeOtherDeviceID] = true
	fake.folders = []folderConfig{{
		ID: "vs-000000000001", Label: "Notes", Path: "/var/syncthing/vaults/notes",
		Devices: []folderDevice{{DeviceID: fakeHubID}, {DeviceID: fakeDeviceID}, {DeviceID: fakeOtherDeviceID}},
	}}
	cfg := statusConfig(t, srv.URL, fake.apiKey, "")

	out, err := captureStdout(t, func() error { return cmdStatus(context.Background(), cfg) })
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	rows := deviceRows(out, "hermes")
	if len(rows) != 2 {
		t.Fatalf("status lists %d rows for hermes, want 2:\n%s", len(rows), out)
	}
	// The first group of a device ID is what Syncthing shows as its short ID;
	// it is what tells one "hermes" from the other.
	for _, short := range []string{"BBBBBBB", "CCCCCCC"} {
		n := 0
		for _, row := range rows {
			if strings.Contains(row, short) {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%d rows name the device %s, want 1:\n%s", n, short, out)
		}
	}
}
