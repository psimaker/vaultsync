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

// A failed read of the pending offers used to be swallowed: the accept went
// ahead and created a local-only folder that no peer shares, looking exactly
// like a successful accept. The caller cannot tell "no offer" from "could
// not read the offers", so the read failure must refuse the accept (#182).
func TestIssue182_AcceptRefusedWhenPendingOffersUnreadable(t *testing.T) {
	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}

	saved := offeringDevicesFor
	t.Cleanup(func() { offeringDevicesFor = saved })
	offeringDevicesFor = func(*syncthing.Internals, string) ([]protocol.DeviceID, error) {
		return nil, errors.New("database is closed")
	}

	folderPath := filepath.Join(configDir, "unreadable-offers-vault")
	errMsg := AcceptPendingFolder("vault-182", "Vault 182", folderPath, false)
	if !strings.Contains(errMsg, "pending folder offers") || !strings.Contains(errMsg, "database is closed") {
		t.Fatalf("AcceptPendingFolder() = %q, want a refusal naming the failed offers read", errMsg)
	}
	if _, err := os.Stat(folderPath); !os.IsNotExist(err) {
		t.Fatalf("folder path exists although the accept was refused (stat err = %v)", err)
	}
	var folders []FolderInfo
	if err := json.Unmarshal([]byte(GetFoldersJSON()), &folders); err != nil {
		t.Fatalf("GetFoldersJSON() unmarshal: %v", err)
	}
	for _, f := range folders {
		if f.ID == "vault-182" {
			t.Fatal("refused accept still added the folder to the config")
		}
	}
}

// A withdrawn offer is not a failed read: the accept still goes ahead with
// the local device only, as before (#182 changes only the error path).
func TestIssue182_AcceptWithoutOfferStillSucceeds(t *testing.T) {
	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	folderPath := filepath.Join(configDir, "withdrawn-offer-vault")
	if errMsg := AcceptPendingFolder("vault-182-b", "Vault", folderPath, false); errMsg != "" {
		t.Fatalf("AcceptPendingFolder() without an offer = %q, want success", errMsg)
	}
}

// An unreadable .stignore used to come back as an empty pattern list, so the
// app showed "no filters" while filters existed on disk (#182). The read
// failure must be visible as such; a missing file is still "no filters".
func TestIssue182_FolderIgnoresReadErrorIsNotEmptyList(t *testing.T) {
	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	const folderID = "vault-182-ignores"
	folderPath := filepath.Join(configDir, folderID)
	if errMsg := AddFolder(folderID, "Vault", folderPath); errMsg != "" {
		t.Fatalf("AddFolder() failed: %s", errMsg)
	}

	if got := GetFolderIgnores(folderID); got != "[]" {
		t.Fatalf("GetFolderIgnores() with no .stignore = %q, want []", got)
	}

	// A directory in place of the file makes ReadFile fail on every platform
	// and as every user, unlike a permission bit.
	if err := os.Mkdir(filepath.Join(folderPath, ".stignore"), 0o700); err != nil {
		t.Fatal(err)
	}
	got := GetFolderIgnores(folderID)
	var asList []string
	if err := json.Unmarshal([]byte(got), &asList); err == nil {
		t.Fatalf("GetFolderIgnores() = %q for an unreadable .stignore, want an error object, not a pattern list", got)
	}
	var asErr struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(got), &asErr); err != nil || asErr.Error == "" {
		t.Fatalf("GetFolderIgnores() = %q, want {\"error\": ...}", got)
	}
	if strings.Contains(asErr.Error, folderPath) {
		t.Fatalf("error text leaks the folder path: %q", asErr.Error)
	}
}
