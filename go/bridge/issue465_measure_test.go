package bridge

// Measurements behind decision 096 (#465): what a write of config.xml costs,
// and how often the bridge's write finishes before the model's removal of a
// folder. They run only on request, and they assert nothing:
//
//   VAULTSYNC_BRIDGE_ISSUE465_MEASURE=1 go test -tags noassets ./bridge -run '^TestIssue465_Measure' -count=1 -v
//
// Run them on the storage in question; a busy machine stretches both sides.

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/syncthing/syncthing/lib/config"
)

func issue465MeasureOn(t *testing.T) {
	if os.Getenv("VAULTSYNC_BRIDGE_ISSUE465_MEASURE") == "" {
		t.Skip("a measurement; set VAULTSYNC_BRIDGE_ISSUE465_MEASURE=1 to run it")
	}
}

func issue465Stats(d []time.Duration) string {
	s := slices.Clone(d)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	var sum time.Duration
	for _, x := range s {
		sum += x
	}
	p := func(q float64) time.Duration { return s[int(q*float64(len(s)-1))] }
	return fmt.Sprintf("n=%d min=%v median=%v p90=%v max=%v mean=%v", len(s), s[0], p(0.5), p(0.9), s[len(s)-1], sum/time.Duration(len(s)))
}

// What one write of config.xml costs, with 1, 10 and 30 folders and two
// devices: 200 writes each.
func TestIssue465_MeasureSave(t *testing.T) {
	issue465MeasureOn(t)
	for _, n := range []int{1, 10, 30} {
		configDir := testConfigDir(t)
		if errMsg := StartSyncthing(configDir); errMsg != "" {
			t.Fatal(errMsg)
		}
		for _, dev := range []string{issue465Peer, "AIR6LPZ-7K4PTTV-UXQSMUU-CPQ5YWH-OEDFIIQ-JUG777G-2YQXXR5-YD6AWQR"} {
			if errMsg := AddDevice(dev, "Peer"); errMsg != "" {
				t.Fatal(errMsg)
			}
		}
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("measure-%02d", i)
			if errMsg := AddFolder(id, "Vault "+id, filepath.Join(configDir, "Vaults", id)); errMsg != "" {
				t.Fatal(errMsg)
			}
		}
		mu.Lock()
		cfg := stCfg
		mu.Unlock()
		var d []time.Duration
		for i := 0; i < 200; i++ {
			start := time.Now()
			if err := cfg.Save(); err != nil {
				t.Fatal(err)
			}
			d = append(d, time.Since(start))
		}
		info, _ := os.Stat(filepath.Join(configDir, "config.xml"))
		t.Logf("Save, %2d folders, config.xml %d bytes: %s", n, info.Size(), issue465Stats(d))
		StopSyncthing()
	}
}

// Who finishes first when the bridge writes right after the engine has taken
// a removal, as commitConfigLocked does: the write, or the model's removal
// (stop the folder, take its marker away, drop its database)? For a folder
// with 1 note and with 1000 notes. "Services done" is when the wait for the
// services ends, just after the model's last step.
func TestIssue465_MeasureTheWriteAgainstTheModelsRemoval(t *testing.T) {
	issue465MeasureOn(t)
	for _, notes := range []int{1, 1000} {
		var modelDone, saveDone []time.Duration
		saveFirst := 0
		const rounds = 20
		for r := 0; r < rounds; r++ {
			configDir := testConfigDir(t)
			if errMsg := StartSyncthing(configDir); errMsg != "" {
				t.Fatal(errMsg)
			}
			id := "measure-removed"
			dir := filepath.Join(configDir, "Vaults", "Kept")
			if errMsg := AddFolder(id, "Vault", dir); errMsg != "" {
				t.Fatal(errMsg)
			}
			for i := 0; i < notes; i++ {
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("note-%04d.md", i)), []byte("# note\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			issue465Poll(t, "the notes in the index", 120*time.Second, func() bool {
				_ = RescanFolder(id)
				return issue465LocalFiles(t, id) == notes
			})
			mu.Lock()
			cfg := stCfg
			start := time.Now()
			waiter, err := cfg.Modify(func(c *config.Configuration) {
				c.Folders = slices.DeleteFunc(c.Folders, func(f config.FolderConfiguration) bool { return f.ID == id })
			})
			if err != nil {
				mu.Unlock()
				t.Fatal(err)
			}
			waited := make(chan time.Duration, 1)
			go func() { waiter.Wait(); waited <- time.Since(start) }()
			if err := cfg.Save(); err != nil {
				mu.Unlock()
				t.Fatal(err)
			}
			s := time.Since(start)
			m := <-waited
			mu.Unlock()
			saveDone = append(saveDone, s)
			modelDone = append(modelDone, m)
			if s < m {
				saveFirst++
			}
			StopSyncthing()
		}
		t.Logf("%4d notes: save done after the change (incl. its fsyncs): %s", notes, issue465Stats(saveDone))
		t.Logf("%4d notes: services done after the change (model's removal, DropFolder): %s", notes, issue465Stats(modelDone))
		t.Logf("%4d notes: the save returned first in %d of %d rounds", notes, saveFirst, rounds)
	}
}
