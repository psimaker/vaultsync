package bridge

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/syncthing"
)

// The app cuts its record of shares set aside with "Ignore for Now" down to
// the list of offers the bridge answers. A stopped engine and a failed read
// answered that list with "[]", the same bytes as a running engine that is
// offered nothing: the app took it for the list and forgot every share it had
// set aside, which the automatic accept then took once the list was read
// again (#464, decision 039). Neither may answer a list.

// pendingFoldersAnswer decodes an answer of GetPendingFoldersJSON: the list,
// or the error text when it is no list.
func pendingFoldersAnswer(t *testing.T, raw string) (list []PendingFolderInfo, isList bool, errText string) {
	t.Helper()
	if err := json.Unmarshal([]byte(raw), &list); err == nil {
		return list, true, ""
	}
	var failure struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(raw), &failure); err != nil || failure.Error == "" {
		t.Fatalf("GetPendingFoldersJSON() = %q: neither a list nor an error", raw)
	}
	return nil, false, failure.Error
}

// failingRead stands in for the engine's read of the offers. The engine's
// result type lives in an internal package of Syncthing, which this module
// cannot name; the type parameter carries it.
func failingRead[R any](_ func(*syncthing.Internals, protocol.DeviceID) (R, error), err error) func(*syncthing.Internals, protocol.DeviceID) (R, error) {
	return func(*syncthing.Internals, protocol.DeviceID) (R, error) {
		var none R
		return none, err
	}
}

func TestIssue464_StoppedEngineAnswerIsNotAList(t *testing.T) {
	// Started and stopped, as at the end of a background run.
	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	if errMsg := StopSyncthing(); errMsg != "" {
		t.Fatalf("StopSyncthing() = %q", errMsg)
	}

	raw := GetPendingFoldersJSON()
	if _, isList, errText := pendingFoldersAnswer(t, raw); isList || errText != "syncthing not running" {
		t.Fatalf("GetPendingFoldersJSON() of a stopped engine = %q, want the not-running error, which the app cannot take for a list", raw)
	}
}

func TestIssue464_FailedReadIsNotAList(t *testing.T) {
	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}

	saved := pendingFoldersRead
	t.Cleanup(func() { pendingFoldersRead = saved })

	pendingFoldersRead = failingRead(saved, errors.New("database is closed"))
	raw := GetPendingFoldersJSON()
	_, isList, errText := pendingFoldersAnswer(t, raw)
	if isList || errText != "read pending folder offers: database is closed" {
		t.Fatalf("GetPendingFoldersJSON() on a failed read = %q, want the read error, never a list", raw)
	}

	// An OS error leaves its path behind: the answer names no path.
	pendingFoldersRead = failingRead(saved, &os.PathError{
		Op:   "open",
		Path: "/private/var/mobile/Containers/Data/Application/Vault Of Someone/index-v2/main.db",
		Err:  errors.New("input/output error"),
	})
	raw = GetPendingFoldersJSON()
	_, isList, errText = pendingFoldersAnswer(t, raw)
	if isList || errText != "read pending folder offers: input/output error" {
		t.Fatalf("GetPendingFoldersJSON() on a failed OS read = %q, want the OS error without its path", raw)
	}
	if strings.Contains(raw, "Vault Of Someone") || strings.Contains(raw, "/private/") {
		t.Fatalf("GetPendingFoldersJSON() = %q names the path", raw)
	}
}

// A read that succeeded and finds nothing keeps its meaning: nothing is
// offered, and the app cuts its record down to that.
func TestIssue464_SuccessfulReadWithoutOffersIsStillAnEmptyList(t *testing.T) {
	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	raw := GetPendingFoldersJSON()
	if raw != "[]" {
		t.Fatalf("GetPendingFoldersJSON() of a running engine without offers = %q, want []", raw)
	}
}

// No answer of the file that reads and accepts the offers names a path: the
// app shows and logs what the bridge answers, and an OS error carries the
// vault's place on this iPhone (safety rule 6). Each answer that passes an
// error on is fed one that carries a path. The configuration commit's error
// is not among them: the engine's configuration answers with folder IDs only.
func TestIssue464_NoAnswerNamesAPath(t *testing.T) {
	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	const secret = "Vault Of Someone"
	root := filepath.Join(configDir, secret)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	namesAPath := func(answer string) bool {
		return strings.Contains(answer, secret) || strings.Contains(answer, configDir)
	}

	// The accept's read of the offers.
	saved := offeringDevicesFor
	t.Cleanup(func() { offeringDevicesFor = saved })
	offeringDevicesFor = func(*syncthing.Internals, string) ([]protocol.DeviceID, error) {
		return nil, &os.PathError{Op: "open", Path: filepath.Join(root, "index-v2", "main.db"), Err: errors.New("input/output error")}
	}
	answer := acceptAsDirectChild("issue464-offers", "Offers", filepath.Join(root, "Offers"), false)
	if answer != "read pending folder offers: input/output error" || namesAPath(answer) {
		t.Fatalf("accept on a failed read of the offers = %q, want the error without its path", answer)
	}
	offeringDevicesFor = saved

	// The accept's read of the target: a file where the folder would be.
	if err := os.WriteFile(filepath.Join(root, "File"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	answer = acceptAsDirectChild("issue464-file", "File", filepath.Join(root, "File"), false)
	if !strings.HasPrefix(answer, "read folder path: ") || namesAPath(answer) {
		t.Fatalf("accept into a file = %q, want the read error without its path", answer)
	}

	// The accept's creation of the target, in a folder it may not write.
	if os.Geteuid() == 0 {
		t.Log("running as root: a folder without write access cannot refuse the creation; that case is left out")
		return
	}
	locked := filepath.Join(root, "Locked")
	if err := os.MkdirAll(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	answer = AcceptPendingFolder("issue464-locked", "Locked", filepath.Join(locked, "Vault"), locked, false)
	if !strings.HasPrefix(answer, "create folder path: ") || namesAPath(answer) {
		t.Fatalf("accept into a folder it may not write = %q, want the creation error without its path", answer)
	}
}
