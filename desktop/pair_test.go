package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/psimaker/vaultsync/hub/pairing"
	"github.com/psimaker/vaultsync/hub/syncthing"
)

// mkVault creates a folder that already holds notes.
func mkVault(t *testing.T, path string) {
	t.Helper()
	writeFile(t, filepath.Join(path, ".obsidian", "app.json"), "{}")
	writeFile(t, filepath.Join(path, "Welcome.md"), "# hello\n")
}

func refusalText(err error) string {
	var r *refusal
	if errors.As(err, &r) {
		return r.msg
	}
	return ""
}

func assertUntouched(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(path, "Welcome.md"))
	if err != nil || string(data) != "# hello\n" {
		t.Fatalf("the local notes were changed: %q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(path, ".stfolder")); err == nil {
		t.Fatal("a Syncthing marker appeared in the local folder")
	}
}

// The second acceptance case of #175: a target folder that already holds
// files never syncs without consent, and never with a vault that is not new.
func TestIssue175_FolderWithFilesNeedsConsent(t *testing.T) {
	ctx := context.Background()

	t.Run("no consent: nothing is asked of the Hub", func(t *testing.T) {
		eng := newFakeEngine(t)
		hub := newFakeHub(t, eng)
		s, _ := testSession(t, eng, hub, pairOptions{code: "", vault: "Notes", create: true})
		s.opts.code = hub.code
		local := filepath.Join(s.env.home, "Notes")
		mkVault(t, local)
		s.opts.path = local
		err := s.run(ctx)
		if !strings.Contains(refusalText(err), "already contains files") || !strings.Contains(refusalText(err), "--yes") {
			t.Fatalf("expected a consent refusal, got %v", err)
		}
		if hub.provisionCount("Notes") != 0 || eng.folderCount() != 0 || len(hub.vaults) != 0 {
			t.Fatalf("something changed without consent: provisions=%d folders=%d hub vaults=%v", hub.provisionCount("Notes"), eng.folderCount(), hub.vaults)
		}
		assertUntouched(t, local)
	})

	t.Run("an existing Hub vault never takes a folder with files", func(t *testing.T) {
		eng := newFakeEngine(t)
		hub := newFakeHub(t, eng, pairing.VaultInfo{ID: "vs-aaaaaaaaaaaa", Label: "Notes", Files: 0})
		s, _ := testSession(t, eng, hub, pairOptions{vault: "Notes", create: true, yes: true})
		s.opts.code = hub.code
		local := filepath.Join(s.env.home, "Notes")
		mkVault(t, local)
		s.opts.path = local
		err := s.run(ctx)
		if !strings.Contains(refusalText(err), "only into a new or empty folder") {
			t.Fatalf("expected the download refusal, got %v", err)
		}
		if hub.provisionCount("vs-aaaaaaaaaaaa")+hub.provisionCount("Notes") != 0 || eng.folderCount() != 0 {
			t.Fatal("the Hub was asked to share, or a folder was added")
		}
		assertUntouched(t, local)
	})

	t.Run("consent: the new vault takes the folder's files as its first copy", func(t *testing.T) {
		eng := newFakeEngine(t)
		hub := newFakeHub(t, eng)
		s, out := testSession(t, eng, hub, pairOptions{vault: "Notes", create: true, yes: true})
		s.opts.code = hub.code
		local := filepath.Join(s.env.home, "Notes")
		mkVault(t, local)
		s.opts.path = local
		if err := s.run(ctx); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if eng.folderCount() != 1 || eng.folders[0].Path != local || eng.folders[0].Label != "Notes" {
			t.Fatalf("folders: %+v", eng.folders)
		}
		if !strings.Contains(out.String(), "is set up to sync at") {
			t.Fatalf("output:\n%s", out)
		}
		if len(eng.patches) != 1 {
			t.Fatalf("a new Hub gets one address hint, got %v", eng.patches)
		}
	})
}

// A populated folder joins only a vault this pairing provably started: the
// agent's own evidence on top of the shared guard's file count.
func TestIssue175_PopulatedFolderNeedsAProvablyNewVault(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		tamper func(*pairing.HubPayload)
	}{
		{"another device shares the new vault", func(p *pairing.HubPayload) {
			for i := range p.Vaults {
				if p.Vaults[i].Label == "Notes" {
					p.Vaults[i].SharedWith = append(p.Vaults[i].SharedWith, otherID)
				}
			}
		}},
		{"the Hub cannot count its files", func(p *pairing.HubPayload) {
			if p.Provisioned != nil {
				p.Provisioned.Files = -1
			}
		}},
		{"the Hub already holds files in it", func(p *pairing.HubPayload) {
			if p.Provisioned != nil {
				p.Provisioned.Files = 3
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eng := newFakeEngine(t)
			hub := newFakeHub(t, eng)
			hub.tamper = c.tamper
			s, out := testSession(t, eng, hub, pairOptions{vault: "Notes", create: true, yes: true})
			s.opts.code = hub.code
			local := filepath.Join(s.env.home, "Notes")
			mkVault(t, local)
			s.opts.path = local
			err := s.run(ctx)
			if !strings.Contains(refusalText(err), "could not confirm that “Notes” is a new, empty vault") {
				t.Fatalf("expected a refusal, got %v\n%s", err, out)
			}
			if eng.folderCount() != 0 {
				t.Fatal("the folder was connected")
			}
			assertUntouched(t, local)
		})
	}

	t.Run("a vault that appeared between the handshake and the request", func(t *testing.T) {
		eng := newFakeEngine(t)
		hub := newFakeHub(t, eng)
		s, _ := testSession(t, eng, hub, pairOptions{vault: "Notes", create: true, yes: true})
		s.opts.code = hub.code
		local := filepath.Join(s.env.home, "Notes")
		mkVault(t, local)
		s.opts.path = local
		// The handshake's catalogue is empty; by the time the agent asks,
		// someone has created "Notes" on the Hub.
		hub.tamper = func(p *pairing.HubPayload) {
			if p.Provisioned == nil && len(p.Vaults) == 0 {
				hub.vaults = append(hub.vaults, pairing.VaultInfo{ID: "vs-bbbbbbbbbbbb", Label: "Notes", Files: 0, SharedWith: []string{otherID}})
				p.Vaults = append(p.Vaults, hub.vaults...)
			}
		}
		err := s.run(ctx)
		if !strings.Contains(refusalText(err), "could not confirm") || hub.provisionCount("Notes") != 0 || eng.folderCount() != 0 {
			t.Fatalf("got %v; provisions=%d folders=%d", err, hub.provisionCount("Notes"), eng.folderCount())
		}
	})
}

// A request whose answer is lost is never sent again on its own (#174's
// rule): the Hub may have acted.
func TestIssue175_UnknownOutcomeIsNeverRetried(t *testing.T) {
	eng := newFakeEngine(t)
	hub := newFakeHub(t, eng, pairing.VaultInfo{ID: "vs-aaaaaaaaaaaa", Label: "Recipes", Files: 12})
	hub.provisionStatus = http.StatusInternalServerError
	hub.offerDelay = -1
	s, _ := testSession(t, eng, hub, pairOptions{vault: "Recipes"})
	s.opts.code = hub.code
	s.opts.path = filepath.Join(s.env.home, "Vaults", "Recipes")
	err := s.run(context.Background())
	if !strings.Contains(refusalText(err), "couldn’t confirm whether your Hub shared “Recipes”") {
		t.Fatalf("got %v", err)
	}
	if n := hub.provisionCount("vs-aaaaaaaaaaaa"); n != 1 {
		t.Fatalf("the share request was sent %d times", n)
	}
	att, _ := loadAttempts(s.env.lay)
	if len(att) != 1 || att[0].State != "unknown" {
		t.Fatalf("the attempt is not recorded as unknown: %+v", att)
	}
}

func TestIssue175_DownloadIntoANewFolder(t *testing.T) {
	eng := newFakeEngine(t)
	hub := newFakeHub(t, eng, pairing.VaultInfo{ID: "vs-aaaaaaaaaaaa", Label: "Recipes", Files: 12})
	// The person picks the Hub vault from the menu and takes the default
	// destination (~/Vaults/Recipes).
	s, out := testSession(t, eng, hub, pairOptions{}, hub.code, "1", "")
	if err := s.run(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := filepath.Join(s.env.home, "Vaults", "Recipes")
	if eng.folderCount() != 1 || eng.folders[0].Path != want || eng.folders[0].ID != "vs-aaaaaaaaaaaa" {
		t.Fatalf("folders: %+v", eng.folders)
	}
	if st, err := os.Stat(want); err != nil || !st.IsDir() {
		t.Fatalf("destination not created: %v", err)
	}
	for _, line := range []string{"✓ Connected to “Test Hub”.", "On your Hub", "Recipes", "12 files on your Hub", "Open folder as vault"} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("output lacks %q:\n%s", line, out)
		}
	}
	if att, _ := loadAttempts(s.env.lay); len(att) != 1 || att[0].State != "accepted" {
		t.Fatalf("attempt: %+v", att)
	}
}

func TestIssue175_WrongCodeCanBeTypedAgain(t *testing.T) {
	eng := newFakeEngine(t)
	hub := newFakeHub(t, eng, pairing.VaultInfo{ID: "vs-aaaaaaaaaaaa", Label: "Recipes", Files: 12})
	parts := strings.Split(hub.code, "-")
	wrong := parts[0] + "-" + parts[1] + "-" + map[bool]string{true: "01", false: "00"}[parts[2] == "00"]
	s, out := testSession(t, eng, hub, pairOptions{}, "not a code", wrong, hub.code, "Q")
	err := s.run(context.Background())
	if !errors.Is(err, errCancelled) {
		t.Fatalf("got %v\n%s", err, out)
	}
	if hub.failures != 1 {
		t.Fatalf("the Hub counted %d wrong codes, want 1 (the malformed one is never sent)", hub.failures)
	}
	for _, line := range []string{"That doesn’t look like a code from your Hub.", "Your Hub didn’t accept this code.", "✓ Connected to “Test Hub”."} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("output lacks %q:\n%s", line, out)
		}
	}
}

// #204 for the agent: several Hubs and no terminal → nobody hears the code.
func TestIssue175_SeveralHubsAreNeverTriedInTurn(t *testing.T) {
	eng := newFakeEngine(t)
	a := newFakeHub(t, eng)
	b := newFakeHub(t, eng)
	s, _ := testSession(t, eng, nil, pairOptions{vault: "Notes", path: "/tmp/x"})
	s.opts.code = b.code
	s.env.discover = func(context.Context) ([]pairing.DiscoveredHub, error) {
		return []pairing.DiscoveredHub{{Address: a.addr, Name: "Hub A"}, {Address: b.addr, Name: "Hub B"}}, nil
	}
	err := s.run(context.Background())
	if !strings.Contains(refusalText(err), "--hub") {
		t.Fatalf("got %v", err)
	}
	if a.starts+b.starts != 0 {
		t.Fatalf("a Hub heard the code: A=%d B=%d", a.starts, b.starts)
	}

	// Discovery answers from outside the local network are dropped.
	s, _ = testSession(t, eng, nil, pairOptions{vault: "Notes", path: "/tmp/x"})
	s.opts.code = a.code
	s.env.discover = func(context.Context) ([]pairing.DiscoveredHub, error) {
		return []pairing.DiscoveredHub{{Address: "203.0.113.5:8390", Name: "Elsewhere"}}, nil
	}
	if err := s.run(context.Background()); !strings.Contains(refusalText(err), "No Hub answered") {
		t.Fatalf("a public address must not count as a Hub: %v", err)
	}
	s, _ = testSession(t, eng, nil, pairOptions{hub: "8.8.8.8"})
	if err := s.run(context.Background()); !strings.Contains(refusalText(err), "not on your local network") {
		t.Fatalf("--hub with a public address: %v", err)
	}
}

func TestIssue175_TargetsAreCheckedBeforeTheHubIsAsked(t *testing.T) {
	cases := []struct {
		name  string
		setup func(s *pairSession) string
		want  string
	}{
		{"a cloud folder", func(s *pairSession) string {
			p := filepath.Join(s.env.home, "Dropbox", "Notes")
			mkVault(t, p)
			return p
		}, "is in Dropbox and cannot sync with VaultSync there"},
		{"VaultSync's own folder", func(s *pairSession) string {
			return filepath.Join(s.env.lay.Base, "Notes")
		}, "VaultSync keeps its own files in"},
		{"a folder around VaultSync's own folder", func(s *pairSession) string {
			return filepath.Join(s.env.home, ".local")
		}, "VaultSync keeps its own files in"},
		{"a folder inside one that already syncs", func(s *pairSession) string {
			synced := filepath.Join(s.env.home, "Work")
			s.env.engine.AddFolder(context.Background(), syncthingFolder("vs-cccccccccccc", "Work", synced))
			return filepath.Join(synced, "Notes")
		}, "which VaultSync already syncs"},
		{"a folder the user's own Syncthing syncs", func(s *pairSession) string {
			p := filepath.Join(s.env.home, "Notes")
			s.env.userST = func() (userSyncthing, bool) {
				return userSyncthing{ConfigPath: "/x/config.xml", Folders: []string{p}}, true
			}
			return p
		}, "already synced by the Syncthing on this computer"},
		{"the user's Syncthing cannot be read", func(s *pairSession) string {
			s.env.userST = func() (userSyncthing, bool) {
				return userSyncthing{ConfigPath: "/x/config.xml", Unreadable: errors.New("bad XML")}, true
			}
			return filepath.Join(s.env.home, "Notes")
		}, "cannot rule out that it already syncs this folder"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eng := newFakeEngine(t)
			hub := newFakeHub(t, eng)
			s, _ := testSession(t, eng, hub, pairOptions{vault: "Notes", create: true, yes: true})
			s.opts.code = hub.code
			s.opts.path = c.setup(s)
			before := eng.folderCount()
			err := s.run(context.Background())
			if !strings.Contains(refusalText(err), c.want) {
				t.Fatalf("expected %q, got %v", c.want, err)
			}
			if hub.provisionCount("Notes") != 0 || eng.folderCount() != before {
				t.Fatal("the Hub was asked, or a folder was added")
			}
		})
	}
}

// The final gate runs after the last wait, right before the folder is
// added: what changed while the agent waited stops the accept.
func TestIssue175_FinalGateRunsAfterTheWait(t *testing.T) {
	ctx := context.Background()
	t.Run("the empty destination gained files", func(t *testing.T) {
		eng := newFakeEngine(t)
		hub := newFakeHub(t, eng, pairing.VaultInfo{ID: "vs-aaaaaaaaaaaa", Label: "Recipes", Files: 12})
		hub.offerDelay = 300 * time.Millisecond
		s, _ := testSession(t, eng, hub, pairOptions{vault: "Recipes"})
		s.opts.code = hub.code
		dest := filepath.Join(s.env.home, "Vaults", "Recipes")
		s.opts.path = dest
		time.AfterFunc(100*time.Millisecond, func() { mkVault(t, dest) })
		// Whichever check sees the files first — the shared guard or the
		// agent's final gate — nothing is added and the files stay.
		err := s.run(ctx)
		if err == nil || eng.folderCount() != 0 {
			t.Fatalf("got %v, folders=%d", err, eng.folderCount())
		}
		assertUntouched(t, dest)
	})
	t.Run("the engine restarted", func(t *testing.T) {
		eng := newFakeEngine(t)
		hub := newFakeHub(t, eng, pairing.VaultInfo{ID: "vs-aaaaaaaaaaaa", Label: "Recipes", Files: 12})
		hub.offerDelay = 300 * time.Millisecond
		s, _ := testSession(t, eng, hub, pairOptions{vault: "Recipes"})
		s.opts.code = hub.code
		s.opts.path = filepath.Join(s.env.home, "Vaults", "Recipes")
		time.AfterFunc(100*time.Millisecond, func() {
			eng.mu.Lock()
			eng.startTime = "2026-10-06T10:05:00Z"
			eng.mu.Unlock()
		})
		err := s.run(ctx)
		if !strings.Contains(refusalText(err), "restarted") || eng.folderCount() != 0 {
			t.Fatalf("got %v, folders=%d", err, eng.folderCount())
		}
	})
	t.Run("the share never arrives", func(t *testing.T) {
		eng := newFakeEngine(t)
		hub := newFakeHub(t, eng, pairing.VaultInfo{ID: "vs-aaaaaaaaaaaa", Label: "Recipes", Files: 12})
		hub.offerDelay = -1
		s, _ := testSession(t, eng, hub, pairOptions{vault: "Recipes"})
		s.opts.code = hub.code
		s.opts.path = filepath.Join(s.env.home, "Vaults", "Recipes")
		s.env.pendingTimeout = time.Second
		err := s.run(ctx)
		if !strings.Contains(refusalText(err), "has not received the share yet") || eng.folderCount() != 0 {
			t.Fatalf("got %v", err)
		}
		if att, _ := loadAttempts(s.env.lay); len(att) != 1 || att[0].State != "shared" {
			t.Fatalf("attempt: %+v", att)
		}
	})
}

// A Hub forgets a session five minutes after the code; past four the agent
// reconnects with the code first instead of losing the request.
func TestIssue175_LongSessionReconnectsFirst(t *testing.T) {
	eng := newFakeEngine(t)
	hub := newFakeHub(t, eng, pairing.VaultInfo{ID: "vs-aaaaaaaaaaaa", Label: "Recipes", Files: 12})
	s, out := testSession(t, eng, hub, pairOptions{vault: "Recipes"})
	s.opts.code = hub.code
	s.opts.path = filepath.Join(s.env.home, "Vaults", "Recipes")
	// The handshake happens at t0; every later look at the clock is five
	// minutes on, as if the person had lingered in the menu.
	t0, calls := time.Now(), 0
	s.env.now = func() time.Time {
		calls++
		if calls == 1 {
			return t0
		}
		return t0.Add(5 * time.Minute)
	}
	if err := s.run(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if hub.starts != 2 || !strings.Contains(out.String(), "Reconnecting to “Test Hub”") {
		t.Fatalf("starts=%d\n%s", hub.starts, out)
	}
	if hub.failures != 0 {
		t.Fatal("the reconnect counted as a wrong code")
	}
}

func TestIssue175_SafeVaultNames(t *testing.T) {
	cases := map[string]string{
		"Notes":            "Notes",
		"  Work / Private": "Work Private",
		"../../etc":        "etc",
		"..hidden":         "hidden",
		"a\x00b\nc":        "a b c",
		"":                 "Vault",
		"C:\\Users":        "C Users",
	}
	for in, want := range cases {
		got := safeVaultName(in)
		if got != want {
			t.Errorf("safeVaultName(%q) = %q, want %q", in, got, want)
		}
		if strings.ContainsAny(got, `/\`) || strings.HasPrefix(got, ".") {
			t.Errorf("safeVaultName(%q) = %q is not one plain folder name", in, got)
		}
	}
}

func TestIssue175_SyncPluginWarnings(t *testing.T) {
	v := filepath.Join(t.TempDir(), "Notes")
	if w := syncPluginWarnings(v); len(w) != 0 {
		t.Fatalf("no .obsidian, no warnings: %v", w)
	}
	// Obsidian turns its core Sync plugin on in every new vault (1.14.4 in
	// the manual test wrote "sync": true for a vault never connected to
	// Obsidian Sync), and the connection itself is kept outside the vault: a
	// warning from core-plugins.json would greet every vault.
	writeFile(t, filepath.Join(v, ".obsidian", "core-plugins.json"), `{"file-explorer":true,"sync":true}`)
	if w := syncPluginWarnings(v); len(w) != 0 {
		t.Fatalf("Obsidian's default core plugins: %v", w)
	}
	writeFile(t, filepath.Join(v, ".obsidian", "community-plugins.json"), `["dataview","remotely-save","obsidian-git"]`)
	w := syncPluginWarnings(v)
	if len(w) != 2 || !strings.Contains(w[0], "Remotely Save") || !strings.Contains(w[1], "Obsidian Git") {
		t.Fatalf("warnings: %v", w)
	}
}

// The Hub answers without a vault list
// (null) when it cannot read its vaults; that is never an empty catalogue.
func TestIssue175_UnreadableCatalogueDecidesNothing(t *testing.T) {
	ctx := context.Background()
	t.Run("at the handshake", func(t *testing.T) {
		eng := newFakeEngine(t)
		hub := newFakeHub(t, eng, pairing.VaultInfo{ID: "vs-aaaaaaaaaaaa", Label: "Notes", Files: 3})
		hub.catalogueUnreadable = true
		s, _ := testSession(t, eng, hub, pairOptions{vault: "Notes", path: "/tmp/never"})
		s.opts.code = hub.code
		err := s.run(ctx)
		if !strings.Contains(refusalText(err), "could not list its vaults") || len(hub.provisions) != 0 {
			t.Fatalf("got %v; provisions=%d", err, len(hub.provisions))
		}
	})
	t.Run("right before starting a vault for a folder with files", func(t *testing.T) {
		eng := newFakeEngine(t)
		hub := newFakeHub(t, eng)
		s, _ := testSession(t, eng, hub, pairOptions{vault: "Notes", create: true, yes: true})
		s.opts.code = hub.code
		local := filepath.Join(s.env.home, "Notes")
		mkVault(t, local)
		s.opts.path = local
		// Readable at the handshake, unreadable from the next request on.
		hub.tamper = func(p *pairing.HubPayload) { p.Vaults = nil }
		err := s.run(ctx)
		if !strings.Contains(refusalText(err), "could not confirm") || hub.provisionCount("Notes") != 0 || eng.folderCount() != 0 {
			t.Fatalf("got %v; provisions=%d folders=%d", err, hub.provisionCount("Notes"), eng.folderCount())
		}
		assertUntouched(t, local)
	})
}

// Consent belongs to the folder the person
// chose — not to whatever sits at that path when the share arrives.
func TestIssue175_ConsentIsBoundToTheFolder(t *testing.T) {
	ctx := context.Background()
	upload := func(t *testing.T) (*fakeEngine, *fakeHub, *pairSession, string) {
		eng := newFakeEngine(t)
		hub := newFakeHub(t, eng)
		s, _ := testSession(t, eng, hub, pairOptions{vault: "Notes", create: true, yes: true})
		s.opts.code = hub.code
		local := filepath.Join(s.env.home, "Notes")
		mkVault(t, local)
		s.opts.path = local
		return eng, hub, s, local
	}
	t.Run("replaced during the last wait", func(t *testing.T) {
		eng, _, s, local := upload(t)
		eng.flap = &offerFlap{back: 2500 * time.Millisecond, during: func() {
			time.Sleep(500 * time.Millisecond) // inside AcceptShare's own wait
			if err := os.Rename(local, local+"-moved"); err != nil {
				t.Error(err)
			}
			mkVault(t, local)
		}}
		err := s.run(ctx)
		if !strings.Contains(refusalText(err), "was replaced") || eng.folderCount() != 0 {
			t.Fatalf("got %v; folders=%d", err, eng.folderCount())
		}
	})
	t.Run("vanished during the wait is never recreated", func(t *testing.T) {
		eng, hub, s, local := upload(t)
		hub.offerDelay = 300 * time.Millisecond
		time.AfterFunc(100*time.Millisecond, func() {
			if err := os.Rename(local, local+"-moved"); err != nil {
				t.Error(err)
			}
		})
		err := s.run(ctx)
		if !strings.Contains(refusalText(err), "disappeared") || eng.folderCount() != 0 {
			t.Fatalf("got %v; folders=%d", err, eng.folderCount())
		}
		if _, err := os.Stat(local); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("the vanished folder was created again")
		}
	})
	t.Run("the place above a new folder changed", func(t *testing.T) {
		eng := newFakeEngine(t)
		hub := newFakeHub(t, eng, pairing.VaultInfo{ID: "vs-aaaaaaaaaaaa", Label: "Recipes", Files: 12})
		hub.offerDelay = 300 * time.Millisecond
		s, _ := testSession(t, eng, hub, pairOptions{vault: "Recipes"})
		s.opts.code = hub.code
		disk := filepath.Join(s.env.home, "Disk")
		if err := os.MkdirAll(disk, 0o755); err != nil {
			t.Fatal(err)
		}
		s.opts.path = filepath.Join(disk, "Recipes")
		// The "disk" goes away and another folder takes its name.
		time.AfterFunc(100*time.Millisecond, func() {
			if err := os.Rename(disk, disk+"-gone"); err != nil {
				t.Error(err)
			}
			if err := os.Mkdir(disk, 0o755); err != nil {
				t.Error(err)
			}
		})
		err := s.run(ctx)
		if !strings.Contains(refusalText(err), "changed while VaultSync was waiting") || eng.folderCount() != 0 {
			t.Fatalf("got %v; folders=%d", err, eng.folderCount())
		}
	})
}

// The Hub side is checked again after every
// wait — another device that joined the new vault meanwhile stops the accept.
func TestIssue175_HubEvidenceIsRefreshedBeforeAdding(t *testing.T) {
	eng := newFakeEngine(t)
	hub := newFakeHub(t, eng)
	hub.offerDelay = 300 * time.Millisecond
	s, _ := testSession(t, eng, hub, pairOptions{vault: "Notes", create: true, yes: true})
	s.opts.code = hub.code
	local := filepath.Join(s.env.home, "Notes")
	mkVault(t, local)
	s.opts.path = local
	time.AfterFunc(150*time.Millisecond, func() {
		hub.mu.Lock()
		defer hub.mu.Unlock()
		for i := range hub.vaults {
			hub.vaults[i].SharedWith = append(hub.vaults[i].SharedWith, otherID)
		}
	})
	err := s.run(context.Background())
	if !strings.Contains(refusalText(err), "still a new, empty vault") || eng.folderCount() != 0 {
		t.Fatalf("got %v; folders=%d", err, eng.folderCount())
	}
	assertUntouched(t, local)
}

// Explicit flags are honoured with a terminal
// too; a folder with files then asks for consent instead of refusing.
func TestIssue175_FlagsWinOverTheMenu(t *testing.T) {
	eng := newFakeEngine(t)
	hub := newFakeHub(t, eng)
	s, out := testSession(t, eng, hub, pairOptions{vault: "Notes", create: true}, "y")
	s.opts.code = hub.code
	local := filepath.Join(s.env.home, "Notes")
	mkVault(t, local)
	s.opts.path = local
	if err := s.run(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out.String(), "Choose a vault to sync") || !strings.Contains(out.String(), "Start syncing? [y/N]") || eng.folderCount() != 1 {
		t.Fatalf("output:\n%s", out)
	}
}

// Nothing is asked of the Hub that could not
// be recorded, and unresolved attempts are never dropped to make room.
func TestIssue175_PairingJournal(t *testing.T) {
	eng := newFakeEngine(t)
	hub := newFakeHub(t, eng, pairing.VaultInfo{ID: "vs-aaaaaaaaaaaa", Label: "Recipes", Files: 12})
	s, _ := testSession(t, eng, hub, pairOptions{vault: "Recipes"})
	s.opts.code = hub.code
	s.opts.path = filepath.Join(s.env.home, "Vaults", "Recipes")
	writeFile(t, attemptsPath(s.env.lay), "{damaged")
	err := s.run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "damaged") || hub.provisionCount("vs-aaaaaaaaaaaa") != 0 {
		t.Fatalf("got %v; provisions=%d", err, hub.provisionCount("vs-aaaaaaaaaaaa"))
	}

	// A record that cannot be made durable stops the pairing before the Hub
	// is asked.
	if err := os.Remove(attemptsPath(s.env.lay)); err != nil {
		t.Fatal(err)
	}
	origSync := syncDir
	syncDir = func(string) error { return errors.New("disk full") }
	err = s.run(context.Background())
	syncDir = origSync
	if err == nil || !strings.Contains(err.Error(), "nothing was sent to your Hub") || hub.provisionCount("vs-aaaaaaaaaaaa") != 0 {
		t.Fatalf("got %v; provisions=%d", err, hub.provisionCount("vs-aaaaaaaaaaaa"))
	}

	var list []pairAttempt
	base := time.Now()
	list = append(list, pairAttempt{Vault: "lost", State: "unknown", At: base})
	for i := 1; i <= 2*attemptsKept; i++ {
		list = append(list, pairAttempt{Vault: "done", State: "accepted", At: base.Add(time.Duration(i) * time.Second)})
	}
	trimmed := trimAttempts(list)
	if len(trimmed) != attemptsKept || trimmed[0].Vault != "lost" {
		t.Fatalf("the unresolved attempt was dropped: %+v", trimmed[0])
	}
}

// The consent belongs to the folder that was
// looked at — not to one put in its place while the question was open, and
// not to one swapped in while the agent asked the Hub one last time.
func TestIssue175_ConsentWindowsAreClosed(t *testing.T) {
	ctx := context.Background()
	swap := func(t *testing.T, path string) {
		t.Helper()
		if err := os.Rename(path, path+"-other"); err != nil {
			t.Fatal(err)
		}
		mkVault(t, path)
	}

	t.Run("replaced while the consent question was open", func(t *testing.T) {
		eng := newFakeEngine(t)
		hub := newFakeHub(t, eng)
		s, _ := testSession(t, eng, hub, pairOptions{vault: "Notes", create: true})
		s.opts.code = hub.code
		local := filepath.Join(s.env.home, "Notes")
		mkVault(t, local)
		s.opts.path = local
		s.t.in = bufio.NewReader(&hookReader{before: func() { swap(t, local) }, r: strings.NewReader("y\n")})
		err := s.run(ctx)
		if !strings.Contains(refusalText(err), "changed while you were answering") {
			t.Fatalf("got %v", err)
		}
		if hub.provisionCount("Notes") != 0 || eng.folderCount() != 0 {
			t.Fatal("the Hub was asked or a folder was added")
		}
	})

	t.Run("replaced during the last Hub request", func(t *testing.T) {
		eng := newFakeEngine(t)
		hub := newFakeHub(t, eng)
		s, _ := testSession(t, eng, hub, pairOptions{vault: "Notes", create: true, yes: true})
		s.opts.code = hub.code
		local := filepath.Join(s.env.home, "Notes")
		mkVault(t, local)
		s.opts.path = local
		// Requests: catalogue before create, create, evidence after create,
		// evidence in the final gate — the folder is swapped during the last.
		hub.onProvision = func(n int, _ pairing.ProvisionPayload) {
			if n == 4 {
				swap(t, local)
			}
		}
		err := s.run(ctx)
		if !strings.Contains(refusalText(err), "was replaced") || eng.folderCount() != 0 {
			t.Fatalf("got %v; folders=%d", err, eng.folderCount())
		}
	})
}

// The gate's last request (the engine's
// folder list) comes before the file-system checks — a swap during it is
// still caught.
func TestIssue175_NoRequestBetweenTheLastChecksAndTheAdd(t *testing.T) {
	eng := newFakeEngine(t)
	hub := newFakeHub(t, eng)
	s, _ := testSession(t, eng, hub, pairOptions{vault: "Notes", create: true, yes: true})
	s.opts.code = hub.code
	local := filepath.Join(s.env.home, "Notes")
	mkVault(t, local)
	s.opts.path = local
	// After the final gate's Hub request, swap the folder inside the next
	// folder-list request — the gate's own.
	hub.onProvision = func(n int, _ pairing.ProvisionPayload) {
		if n == 4 {
			eng.mu.Lock()
			eng.onNextFolders = func() {
				if err := os.Rename(local, local+"-other"); err != nil {
					t.Error(err)
				}
				mkVault(t, local)
			}
			eng.mu.Unlock()
		}
	}
	err := s.run(context.Background())
	if !strings.Contains(refusalText(err), "was replaced") || eng.folderCount() != 0 {
		t.Fatalf("got %v; folders=%d", err, eng.folderCount())
	}
}

// The menu's own path (Another folder…) binds
// the answers to the folder too.
func TestIssue175_MenuAnswersBelongToTheFolder(t *testing.T) {
	eng := newFakeEngine(t)
	hub := newFakeHub(t, eng)
	s, out := testSession(t, eng, hub, pairOptions{})
	s.opts.code = hub.code
	local := filepath.Join(s.env.home, "Notes")
	mkVault(t, local)
	// Menu: A (another folder), the folder, the name (default), then the
	// consent — the folder is swapped right before that last answer.
	s.t.in = bufio.NewReader(&lineReader{
		lines: []string{"A", local, "", "y"},
		before: map[int]func(){3: func() {
			if err := os.Rename(local, local+"-other"); err != nil {
				t.Error(err)
			}
			mkVault(t, local)
		}},
	})
	_ = s.run(context.Background()) // the menu comes back, then input ends
	if !strings.Contains(out.String(), "changed while you were answering") {
		t.Fatalf("output:\n%s", out)
	}
	if hub.provisionCount("Notes") != 0 || eng.folderCount() != 0 {
		t.Fatal("the Hub was asked or a folder was added")
	}
}

// Setup run again with the same flags — as a script would — meets the vault
// it already set up (#228): at the same folder there is nothing to do and
// the setup succeeds; at another folder it says where the vault syncs. Both
// before the Hub is asked. Another vault into a folder inside the synced one
// is still an overlap.
func TestIssue228_SetupAgainWithTheSameFlags(t *testing.T) {
	ctx := context.Background()
	const id = "vs-aaaaaaaaaaaa"
	configured := func(t *testing.T, vault, path string) (*fakeEngine, *fakeHub, *pairSession, *bytes.Buffer) {
		eng := newFakeEngine(t)
		hub := newFakeHub(t, eng,
			pairing.VaultInfo{ID: id, Label: "Hub-Test", Files: 3, SharedWith: []string{testMyID}},
			pairing.VaultInfo{ID: "vs-bbbbbbbbbbbb", Label: "Work", Files: 5})
		s, out := testSession(t, eng, hub, pairOptions{vault: vault})
		s.opts.code = hub.code
		local := filepath.Join(s.env.home, "Vaults", "Hub-Test-neu")
		mkVault(t, local)
		eng.mu.Lock()
		eng.folders = append(eng.folders, syncthing.FolderConfig{ID: id, Label: "Hub-Test", Path: local})
		eng.mu.Unlock()
		s.opts.path = path
		return eng, hub, s, out
	}
	t.Run("the same folder: nothing to do", func(t *testing.T) {
		eng, hub, s, out := configured(t, "Hub-Test", "~/Vaults/Hub-Test-neu")
		if err := s.run(ctx); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if !strings.Contains(out.String(), "✓ “Hub-Test” is already set up to sync at ~/Vaults/Hub-Test-neu.") || strings.Contains(out.String(), "overlaps") {
			t.Fatalf("output:\n%s", out)
		}
		if hub.provisionCount(id) != 0 || eng.folderCount() != 1 {
			t.Fatalf("the Hub was asked (%d) or a folder was added (%d)", hub.provisionCount(id), eng.folderCount())
		}
		assertUntouched(t, filepath.Join(s.env.home, "Vaults", "Hub-Test-neu"))
	})
	t.Run("another folder: one folder per vault", func(t *testing.T) {
		eng, hub, s, _ := configured(t, "Hub-Test", "~/Vaults/Elsewhere")
		err := s.run(ctx)
		if !strings.Contains(refusalText(err), "“Hub-Test” is already set up to sync at ~/Vaults/Hub-Test-neu on this computer") || strings.Contains(refusalText(err), "overlaps") {
			t.Fatalf("got %v", err)
		}
		if hub.provisionCount(id) != 0 || eng.folderCount() != 1 {
			t.Fatalf("the Hub was asked (%d) or a folder was added (%d)", hub.provisionCount(id), eng.folderCount())
		}
		if _, err := os.Stat(filepath.Join(s.env.home, "Vaults", "Elsewhere")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("a folder was created")
		}
	})
	t.Run("the same folder through a link", func(t *testing.T) {
		eng, hub, s, out := configured(t, "Hub-Test", "~/Links/notes")
		if err := os.MkdirAll(filepath.Join(s.env.home, "Links"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(s.env.home, "Vaults", "Hub-Test-neu"), filepath.Join(s.env.home, "Links", "notes")); err != nil {
			t.Fatal(err)
		}
		if err := s.run(ctx); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if !strings.Contains(out.String(), "✓ “Hub-Test” is already set up to sync at ~/Vaults/Hub-Test-neu.") || hub.provisionCount(id) != 0 || eng.folderCount() != 1 {
			t.Fatalf("output:\n%s", out)
		}
	})
	t.Run("a name that differs only in case is another folder on a case-sensitive volume", func(t *testing.T) {
		eng, hub, s, out := configured(t, "Hub-Test", "~/Vaults/hub-test-neu")
		other := filepath.Join(s.env.home, "Vaults", "hub-test-neu")
		if err := os.MkdirAll(other, 0o755); err != nil {
			t.Fatal(err)
		}
		a, _ := os.Stat(other)
		b, _ := os.Stat(filepath.Join(s.env.home, "Vaults", "Hub-Test-neu"))
		if os.SameFile(a, b) {
			t.Skip("a case-insensitive volume: both names are one folder")
		}
		s.env.goos = "darwin" // where case used to be folded by OS, not by the volume
		if err := s.run(ctx); err == nil || strings.Contains(out.String(), "is already set up to sync") {
			t.Fatalf("two folders were taken for one: %v\n%s", err, out)
		}
		if hub.provisionCount(id) != 0 || eng.folderCount() != 1 {
			t.Fatalf("the Hub was asked (%d) or a folder was added (%d)", hub.provisionCount(id), eng.folderCount())
		}
	})
	t.Run("the same folder under another spelling of its case", func(t *testing.T) {
		eng, hub, s, out := configured(t, "Hub-Test", "~/Vaults/hub-test-neu")
		a, errA := os.Stat(filepath.Join(s.env.home, "Vaults", "hub-test-neu"))
		b, errB := os.Stat(filepath.Join(s.env.home, "Vaults", "Hub-Test-neu"))
		if errA != nil || errB != nil || !os.SameFile(a, b) {
			t.Skip("a case-sensitive volume: another spelling is another folder")
		}
		// Two spellings, one directory: only its identity says so.
		if err := s.run(ctx); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if !strings.Contains(out.String(), "✓ “Hub-Test” is already set up to sync at ~/Vaults/Hub-Test-neu.") || hub.provisionCount(id) != 0 || eng.folderCount() != 1 {
			t.Fatalf("output:\n%s", out)
		}
	})
	t.Run("a file at the chosen path is not the folder", func(t *testing.T) {
		eng, hub, s, out := configured(t, "Hub-Test", "~/Vaults/note.md")
		writeFile(t, filepath.Join(s.env.home, "Vaults", "note.md"), "# a note\n")
		err := s.run(ctx)
		if err == nil || strings.Contains(out.String(), "is already set up to sync") {
			t.Fatalf("a file was taken for the folder: %v\n%s", err, out)
		}
		if hub.provisionCount(id) != 0 || eng.folderCount() != 1 {
			t.Fatalf("the Hub was asked (%d) or a folder was added (%d)", hub.provisionCount(id), eng.folderCount())
		}
	})
	t.Run("a pending share resumed onto its own folder", func(t *testing.T) {
		eng := newFakeEngine(t)
		hub := newFakeHub(t, eng)
		s, out := testSession(t, eng, hub, pairOptions{}, "y", "~/Vaults/Hub-Test-neu")
		local := filepath.Join(s.env.home, "Vaults", "Hub-Test-neu")
		mkVault(t, local)
		eng.mu.Lock()
		eng.devices = append(eng.devices, syncthing.DeviceConfig{DeviceID: testHubID, Name: "Test Hub"})
		eng.folders = append(eng.folders, syncthing.FolderConfig{ID: id, Label: "Hub-Test", Path: local})
		eng.pending[id] = map[string]string{testHubID: "Hub-Test"}
		eng.mu.Unlock()
		if err := s.run(ctx); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if !strings.Contains(out.String(), "✓ “Hub-Test” is already set up to sync at ~/Vaults/Hub-Test-neu.") || strings.Contains(out.String(), "error") || eng.folderCount() != 1 {
			t.Fatalf("output:\n%s", out)
		}
	})
	t.Run("another folder that holds files: the merge guard speaks first", func(t *testing.T) {
		eng, hub, s, _ := configured(t, "Hub-Test", "~/Vaults/Full")
		mkVault(t, filepath.Join(s.env.home, "Vaults", "Full"))
		err := s.run(ctx)
		if !strings.Contains(refusalText(err), "only into a new or empty folder") {
			t.Fatalf("got %v", err)
		}
		if hub.provisionCount(id) != 0 || eng.folderCount() != 1 {
			t.Fatalf("the Hub was asked (%d) or a folder was added (%d)", hub.provisionCount(id), eng.folderCount())
		}
		assertUntouched(t, filepath.Join(s.env.home, "Vaults", "Full"))
	})
	t.Run("another vault into a folder inside the synced one is still an overlap", func(t *testing.T) {
		eng, hub, s, _ := configured(t, "Work", "~/Vaults/Hub-Test-neu/Notes")
		err := s.run(ctx)
		if !strings.Contains(refusalText(err), "overlaps ~/Vaults/Hub-Test-neu, which VaultSync already syncs") {
			t.Fatalf("got %v", err)
		}
		if hub.provisionCount("vs-bbbbbbbbbbbb") != 0 || eng.folderCount() != 1 {
			t.Fatalf("the Hub was asked (%d) or a folder was added (%d)", hub.provisionCount(id), eng.folderCount())
		}
	})
}
