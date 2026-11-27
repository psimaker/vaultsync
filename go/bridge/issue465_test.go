package bridge

import (
	"bufio"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/syncthing"
)

// Every change the bridge makes to the engine's configuration goes through
// commitConfigLocked, and the bridge answers once the engine has the change.
// config.xml, the file the next start loads, used to be written only by the
// configuration wrapper's five-second timer and at a stop (#302). An app
// that ends in those seconds — closed in the app switcher, or ended by iOS,
// neither of which stops the engine — starts the next time from a file
// without the change (#465).

const (
	issue465Peer     = "MFZWI3D-BONSGYC-YLTMRWG-C43ENR5-QXGZDMM-FZWI3DP-BONSGYY-LTMRWAD"
	issue465Removed  = "issue465-removed"
	issue465Accepted = "issue465-accepted"
	issue465Added    = "issue465-added"
)

// Each call is followed at once by a read of config.xml: no stop, no sleep,
// no poll. What the bridge reported as done must be what the next start
// loads.
func TestIssue465_EachChangeIsInConfigXMLWhenTheCallReturns(t *testing.T) {
	configDir := testConfigDir(t)
	root := filepath.Join(configDir, "Vaults")
	// The vault this test removes is on disk before the test begins: a
	// removal of a vault that never reached the file proves nothing.
	issue465StartWithVaultOnDisk(t, configDir, issue465Removed, filepath.Join(root, "Kept"))
	issue465OfferFrom(t, issue465Peer)

	lacking := 0
	expect := func(step string, holds func(issue465OnDisk) bool) {
		t.Helper()
		onDisk, err := issue465ReadConfigXML(configDir)
		if err != nil || !holds(onDisk) {
			lacking++
			t.Errorf("%s: config.xml lacked the change when the call returned (read error: %v)", step, err)
		}
	}

	if errMsg := AddDevice(issue465Peer, "Peer"); errMsg != "" {
		t.Fatalf("AddDevice() = %q", errMsg)
	}
	expect("AddDevice", func(d issue465OnDisk) bool { return d.devices[issue465Peer] })

	if errMsg := AddFolder(issue465Added, "Vault", filepath.Join(root, "Added")); errMsg != "" {
		t.Fatalf("AddFolder() = %q", errMsg)
	}
	expect("AddFolder", func(d issue465OnDisk) bool { _, ok := d.folders[issue465Added]; return ok })

	if errMsg := AcceptPendingFolder(issue465Accepted, "Shared", filepath.Join(root, "Shared"), root, false); errMsg != "" {
		t.Fatalf("AcceptPendingFolder() = %q", errMsg)
	}
	expect("AcceptPendingFolder", func(d issue465OnDisk) bool {
		f, ok := d.folders[issue465Accepted]
		return ok && f.devices[issue465Peer]
	})

	// The launch shield pauses a folder once and records that it did (#45):
	// a pause that is not on disk runs the folder again at the next start,
	// and the record keeps the shield from pausing it a second time.
	if errMsg := SetFolderPaused(issue465Accepted, true); errMsg != "" {
		t.Fatalf("SetFolderPaused() = %q", errMsg)
	}
	expect("SetFolderPaused", func(d issue465OnDisk) bool { return d.folders[issue465Accepted].paused })

	if errMsg := RemoveFolder(issue465Removed); errMsg != "" {
		t.Fatalf("RemoveFolder() = %q", errMsg)
	}
	expect("RemoveFolder", func(d issue465OnDisk) bool { _, ok := d.folders[issue465Removed]; return !ok })

	if lacking > 0 {
		t.Logf("%d of 5 calls returned before config.xml held their change", lacking)
	}
}

// The way an app ends when it is closed in the app switcher or ended by
// iOS: no stop, the process is killed. The next start loads config.xml and
// drops the database of every folder it does not name. A vault removed and
// a share accepted just before the kill must come back as the bridge
// answered them: the removed one not configured, the accepted one
// configured with the index it had.
func TestIssue465_AKillRightAfterTheAnswersKeepsWhatTheBridgeAnswered(t *testing.T) {
	if os.Getenv(issue465ChildEnv) != "" {
		configDir := os.Getenv(issue465DirEnv)
		root := filepath.Join(configDir, "Vaults")
		issue465StartWithVaultOnDisk(t, configDir, issue465Removed, filepath.Join(root, "Kept"))
		removedIndex := issue465IndexID(t, issue465Removed)
		issue465OfferFrom(t, issue465Peer)
		if errMsg := AcceptPendingFolder(issue465Accepted, "Shared", filepath.Join(root, "Shared"), root, false); errMsg != "" {
			t.Fatalf("AcceptPendingFolder() = %q", errMsg)
		}
		issue465IndexNote(t, issue465Accepted, filepath.Join(root, "Shared"))
		acceptedIndex := issue465IndexID(t, issue465Accepted)
		if errMsg := RemoveFolder(issue465Removed); errMsg != "" {
			t.Fatalf("RemoveFolder() = %q", errMsg)
		}
		issue465Say("removed-index", removedIndex.String())
		issue465Say("accepted-index", acceptedIndex.String())
		issue465ReadyToDie()
	}

	configDir := testConfigDir(t)
	said := issue465KillChildWhenReady(t, "^TestIssue465_AKillRightAfterTheAnswersKeepsWhatTheBridgeAnswered$", configDir)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() after the kill = %q", errMsg)
	}
	configured := configuredFolderIDs(t)
	if configured[issue465Removed] {
		t.Errorf("the vault removed before the kill is configured again: index %s before the removal, %s now, state %q",
			said["removed-index"], issue465IndexID(t, issue465Removed), issue465FolderState(t, issue465Removed))
	}
	if !configured[issue465Accepted] {
		t.Errorf("the share accepted before the kill is not configured, and the start dropped its database (database lists it: %t)",
			issue465DatabaseLists(t, issue465Accepted))
	} else if now := issue465IndexID(t, issue465Accepted).String(); now != said["accepted-index"] {
		t.Errorf("the share accepted before the kill has index %s, want the %s it had", now, said["accepted-index"])
	}
}

// The engine takes a change into its configuration first and then waits for
// every service: the model stops a removed folder, takes away its marker and
// drops its database there. Another service that is slow with the same
// change holds the wait open after the model has dropped the database, and
// the bridge answers once its own wait ends (#343). A kill in that time must
// not bring the vault back: configured again with no index, the engine
// takes it for a new folder, makes its marker again and syncs it — into a
// directory that holds files, without the question an accept asks.
func TestIssue465_AKillAfterTheModelDroppedARemovedFolderDoesNotBringItBack(t *testing.T) {
	if os.Getenv(issue465ChildEnv) != "" {
		configDir := os.Getenv(issue465DirEnv)
		issue465StartWithVaultOnDisk(t, configDir, issue465Removed, filepath.Join(configDir, "Vaults", "Kept"))
		removedIndex := issue465IndexID(t, issue465Removed)
		held := newHeldCommitter(removesFolder(issue465Removed))
		subscribeHeld(t, held)
		lifecycleTimeouts.configCommit = 300 * time.Millisecond
		answer := RemoveFolder(issue465Removed)
		waitFor(t, "the model to drop the removed folder's database", 10*time.Second, func() bool {
			return !issue465DatabaseLists(t, issue465Removed)
		})
		select {
		case <-held.entered:
		default:
			t.Fatal("the removal's commit was not held")
		}
		issue465Say("answer", fmt.Sprintf("%q", answer))
		issue465Say("removed-index", removedIndex.String())
		issue465ReadyToDie()
	}

	configDir := testConfigDir(t)
	said := issue465KillChildWhenReady(t, "^TestIssue465_AKillAfterTheModelDroppedARemovedFolderDoesNotBringItBack$", configDir)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() after the kill = %q", errMsg)
	}
	if configuredFolderIDs(t)[issue465Removed] {
		t.Errorf("the vault removed before the kill (bridge answer %s, its database already dropped) is configured again: "+
			"index %s before the removal, %s now, state %q", said["answer"], said["removed-index"],
			issue465IndexID(t, issue465Removed), issue465FolderState(t, issue465Removed))
	}
}

// A removal queued behind another change has not reached the configuration
// when the bridge's wait ends (#343). A kill before it lands must leave the
// vault exactly as it was: configured, with the index it had. Holds today;
// it fails for any design that writes a change to the file before the
// engine has taken it.
func TestIssue465_AKillBeforeAQueuedRemovalLandsLeavesTheVaultAsItWas(t *testing.T) {
	if os.Getenv(issue465ChildEnv) != "" {
		configDir := os.Getenv(issue465DirEnv)
		issue465StartWithVaultOnDisk(t, configDir, issue465Removed, filepath.Join(configDir, "Vaults", "Kept"))
		removedIndex := issue465IndexID(t, issue465Removed)
		issue465HoldTheConfigurationLoop(t)
		lifecycleTimeouts.configCommit = 300 * time.Millisecond
		if answer := RemoveFolder(issue465Removed); answer != FolderRemovalNotConfirmedError() {
			t.Fatalf("RemoveFolder() = %q, want %q", answer, FolderRemovalNotConfirmedError())
		}
		issue465Say("removed-index", removedIndex.String())
		issue465ReadyToDie()
	}

	configDir := testConfigDir(t)
	said := issue465KillChildWhenReady(t, "^TestIssue465_AKillBeforeAQueuedRemovalLandsLeavesTheVaultAsItWas$", configDir)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() after the kill = %q", errMsg)
	}
	if !configuredFolderIDs(t)[issue465Removed] {
		t.Fatal("a removal that never reached the configuration took the vault away")
	}
	if now := issue465IndexID(t, issue465Removed).String(); now != said["removed-index"] {
		t.Errorf("the vault has index %s after the kill, want the %s it had", now, said["removed-index"])
	}
}

// A change that is still queued when the bridge's wait ends lands later
// (#343). It must reach config.xml when it lands, not when the wrapper's
// timer next runs: the app has already moved on, and may be ended at any
// moment. The timer is armed by the change held in front of it, so it would
// write about five seconds after that one; the bound here is half of that.
func TestIssue465_ARemovalThatLandsAfterTheWaitIsOnDiskWhenItLands(t *testing.T) {
	configDir := testConfigDir(t)
	issue465StartWithVaultOnDisk(t, configDir, issue465Removed, filepath.Join(configDir, "Vaults", "Kept"))
	held := issue465HoldTheConfigurationLoop(t)

	defer overrideLifecycleTimeouts(t)()
	lifecycleTimeouts.configCommit = 300 * time.Millisecond
	if answer := RemoveFolder(issue465Removed); answer != FolderRemovalNotConfirmedError() {
		t.Fatalf("RemoveFolder() = %q, want %q", answer, FolderRemovalNotConfirmedError())
	}
	held.let()

	issue465Poll(t, "the queued removal to land", 10*time.Second, func() bool {
		return !configuredFolderIDs(t)[issue465Removed]
	})
	landed := time.Now()
	const bound = 2500 * time.Millisecond
	for !issue302PersistedWithout(configDir, issue465Removed) {
		if time.Since(landed) > bound {
			t.Fatalf("config.xml still named the vault %v after its removal landed: only the wrapper's timer writes it", bound)
		}
		time.Sleep(time.Millisecond)
	}
	t.Logf("config.xml lacked the vault %v after its removal landed", time.Since(landed))
}

// The file can name a removal before the model has dropped the folder's
// database: the start that loads it drops the database of every folder it
// does not name, and the vault stays removed. Holds today; it is the order a
// write before the wait for the services makes possible.
func TestIssue465_AStartCompletesARemovalTheFileNamesBeforeTheDatabaseDropsIt(t *testing.T) {
	configDir := testConfigDir(t)
	dir := filepath.Join(configDir, "Vaults", "Kept")
	issue465StartWithVaultOnDisk(t, configDir, issue465Removed, dir)
	myID, err := protocol.DeviceIDFromString(DeviceID())
	if err != nil {
		t.Fatal(err)
	}
	if errMsg := StopSyncthing(); errMsg != "" {
		t.Fatalf("StopSyncthing() = %q", errMsg)
	}

	// config.xml as the bridge's write leaves it, the database as the model
	// left it before it dropped the folder.
	path := filepath.Join(configDir, "config.xml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := config.ReadXML(bytes.NewReader(raw), myID)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Folders = slices.DeleteFunc(cfg.Folders, func(f config.FolderConfiguration) bool { return f.ID == issue465Removed })
	var out bytes.Buffer
	if err := cfg.WriteXML(&out); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() = %q", errMsg)
	}
	if configuredFolderIDs(t)[issue465Removed] {
		t.Fatal("the vault the file no longer names is configured")
	}
	if issue465DatabaseLists(t, issue465Removed) {
		t.Fatal("the start kept the database of a folder the file no longer names")
	}
	if _, err := os.Stat(filepath.Join(dir, "note.md")); err != nil {
		t.Fatalf("the vault's note: %v", err)
	}
	_, markerErr := os.Stat(filepath.Join(dir, config.DefaultMarkerName))
	t.Logf("the vault's directory keeps its note; its marker is there: %t", markerErr == nil)
}

// --- helpers -----------------------------------------------------------------

const (
	issue465ChildEnv = "VAULTSYNC_BRIDGE_ISSUE465_CHILD"
	issue465DirEnv   = "VAULTSYNC_BRIDGE_ISSUE465_DIR"
	issue465Said     = "issue465-said:"
	issue465Ready    = "issue465-ready-to-die"
)

// issue465OnDisk is what config.xml names: what the next start loads.
type issue465OnDisk struct {
	folders map[string]issue465DiskFolder
	devices map[string]bool
}

type issue465DiskFolder struct {
	paused  bool
	devices map[string]bool
}

// issue465ReadConfigXML reads config.xml as it lies on disk. A file that
// cannot be read or parsed is an error, never an empty configuration.
func issue465ReadConfigXML(configDir string) (issue465OnDisk, error) {
	raw, err := os.ReadFile(filepath.Join(configDir, "config.xml"))
	if err != nil {
		return issue465OnDisk{}, err
	}
	var cfg struct {
		Folders []struct {
			ID      string `xml:"id,attr"`
			Paused  bool   `xml:"paused"`
			Devices []struct {
				ID string `xml:"id,attr"`
			} `xml:"device"`
		} `xml:"folder"`
		Devices []struct {
			ID string `xml:"id,attr"`
		} `xml:"device"`
	}
	if err := xml.Unmarshal(raw, &cfg); err != nil {
		return issue465OnDisk{}, err
	}
	onDisk := issue465OnDisk{folders: map[string]issue465DiskFolder{}, devices: map[string]bool{}}
	for _, f := range cfg.Folders {
		folder := issue465DiskFolder{paused: f.Paused, devices: map[string]bool{}}
		for _, d := range f.Devices {
			folder.devices[d.ID] = true
		}
		onDisk.folders[f.ID] = folder
	}
	for _, d := range cfg.Devices {
		onDisk.devices[d.ID] = true
	}
	return onDisk, nil
}

// issue465StartWithVaultOnDisk starts the engine in configDir with the
// vault id in dir, one note indexed, and leaves it running with the vault in
// config.xml: the stop in between writes it (#302).
func issue465StartWithVaultOnDisk(t *testing.T, configDir, id, dir string) {
	t.Helper()
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() = %q", errMsg)
	}
	if errMsg := AddFolder(id, "Vault", dir); errMsg != "" {
		t.Fatalf("AddFolder() = %q", errMsg)
	}
	issue465IndexNote(t, id, dir)
	if errMsg := StopSyncthing(); errMsg != "" {
		t.Fatalf("StopSyncthing() = %q", errMsg)
	}
	if !issue302Persisted(configDir, id) {
		t.Fatal("config.xml lacks the vault after the stop")
	}
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() again = %q", errMsg)
	}
	issue465Poll(t, "the vault's note in its index after the restart", 30*time.Second, func() bool {
		return issue465LocalFiles(t, id) == 1
	})
}

// issue465IndexNote writes a note into the folder's directory and waits for
// the folder's index to hold it.
func issue465IndexNote(t *testing.T, id, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "note.md"), []byte("# A note\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	issue465Poll(t, "the note in the folder's index", 30*time.Second, func() bool {
		_ = RescanFolder(id)
		return issue465LocalFiles(t, id) == 1
	})
}

func issue465LocalFiles(t *testing.T, id string) int {
	t.Helper()
	var st FolderStatus
	if err := json.Unmarshal([]byte(GetFolderStatusJSON(id)), &st); err != nil {
		return -1
	}
	return st.LocalFiles
}

func issue465FolderState(t *testing.T, id string) string {
	t.Helper()
	var st FolderStatus
	if err := json.Unmarshal([]byte(GetFolderStatusJSON(id)), &st); err != nil {
		return "unreadable"
	}
	return st.State
}

// issue465OfferFrom makes every accept find the share offered by peer, the
// way a peer's offer reaches the engine.
func issue465OfferFrom(t *testing.T, peer string) {
	t.Helper()
	id, err := protocol.DeviceIDFromString(peer)
	if err != nil {
		t.Fatal(err)
	}
	saved := offeringDevicesFor
	t.Cleanup(func() { offeringDevicesFor = saved })
	offeringDevicesFor = func(*syncthing.Internals, string) ([]protocol.DeviceID, error) {
		return []protocol.DeviceID{id}, nil
	}
}

// issue465HoldTheConfigurationLoop makes an unrelated change and holds its
// commit, so the next change waits in the engine's queue (#343).
func issue465HoldTheConfigurationLoop(t *testing.T) *heldCommitter {
	t.Helper()
	const marker = 46546
	held := newHeldCommitter(func(from, to config.Configuration) bool {
		return to.Options.MaxRecvKbps == marker && from.Options.MaxRecvKbps != marker
	})
	subscribeHeld(t, held)
	mu.Lock()
	cfg := stCfg
	mu.Unlock()
	go func() {
		if waiter, err := cfg.Modify(func(c *config.Configuration) { c.Options.MaxRecvKbps = marker }); err == nil {
			waiter.Wait()
		}
	}()
	select {
	case <-held.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the unrelated change never reached its commit")
	}
	return held
}

// issue465IndexID reads the folder's local index ID. It must only be asked
// for a folder the engine has: the database makes one for any other.
func issue465IndexID(t *testing.T, id string) protocol.IndexID {
	t.Helper()
	index, err := issue315Store(t).GetIndexID(id, protocol.LocalDeviceID)
	if err != nil {
		t.Fatalf("GetIndexID: %v", err)
	}
	return index
}

// issue465DatabaseLists reports whether the engine's database holds a folder.
func issue465DatabaseLists(t *testing.T, id string) bool {
	t.Helper()
	lister, ok := issue315Store(t).(interface{ ListFolders() ([]string, error) })
	if !ok {
		t.Fatal("the engine's database cannot list its folders — adjust the test for this Syncthing version")
	}
	folders, err := lister.ListFolders()
	if err != nil {
		t.Fatalf("ListFolders: %v", err)
	}
	return slices.Contains(folders, id)
}

// issue465Poll waits for cond, looking every few milliseconds. It is for
// the setup of a case; the reads a case asserts on follow its call at once.
func issue465Poll(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func issue465Say(key, value string) {
	fmt.Printf("%s%s=%s\n", issue465Said, key, value)
}

// issue465ReadyToDie tells the parent the child has done its part and waits
// for the kill. The engine is left running, as in an app that is ended.
func issue465ReadyToDie() {
	fmt.Println(issue465Ready)
	time.Sleep(time.Hour)
}

// issue465KillChildWhenReady runs this test binary again as a child for the
// test named by pattern, in configDir, and kills it with SIGKILL the moment
// it says it is ready: what the child's engine left on disk is then what a
// killed app leaves. Returns what the child said.
func issue465KillChildWhenReady(t *testing.T, pattern, configDir string) map[string]string {
	t.Helper()
	if testing.Short() {
		t.Skip("kills a child process running the engine; skipped in -short")
	}
	child := exec.Command(os.Args[0], "-test.run="+pattern, "-test.count=1", "-test.v")
	child.Env = append(os.Environ(), issue465ChildEnv+"=1", issue465DirEnv+"="+configDir)
	var stderr bytes.Buffer
	child.Stderr = &stderr
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	said := map[string]string{}
	var all strings.Builder
	ready := make(chan bool, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			all.WriteString(line + "\n")
			if kv, ok := strings.CutPrefix(line, issue465Said); ok {
				if k, v, ok := strings.Cut(kv, "="); ok {
					said[k] = v
				}
			}
			if line == issue465Ready {
				ready <- true
				return
			}
		}
		ready <- false
	}()
	var ok, answered bool
	select {
	case ok = <-ready:
		answered = true
	case <-time.After(3 * time.Minute):
	}
	readyAt := time.Now()
	_ = child.Process.Kill()
	killedIn := time.Since(readyAt)
	_ = child.Wait()
	if !answered {
		<-ready // the reader is done with what the child wrote
	}
	if !ok {
		t.Fatalf("the child never got ready:\n--- its output ---\n%s--- its stderr ---\n%s", all.String(), stderr.String())
	}
	t.Logf("killed the child %v after it said it was ready", killedIn)
	return said
}
