package tray

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// #281: beside an agent.json in a newer format that it cannot read, the
// menu says that it is older and how to update it — never that the file
// is damaged, which a person may delete — and names what the person has:
// VaultSync for Mac on a Mac, the VaultSync menu-bar app on Linux.
func TestIssue281_TheMenuSaysThisAppIsOlder(t *testing.T) {
	for _, c := range []struct {
		mac      bool
		headline string
	}{
		{true, "This VaultSync for Mac is older than the one set up here"},
		{false, "This VaultSync menu-bar app is older than the VaultSync set up here"},
	} {
		v := Render(Status{}, ErrOlderApp, Options{CanStart: true, Mac: c.mac})
		if v.Glyph != GlyphAttention || v.Headline != c.headline || v.Detail != "Run the setup link to update it" {
			t.Fatalf("the menu beside a newer agent.json (Mac %v): %+v", c.mac, v)
		}
		if !c.mac && strings.Contains(v.Headline+v.Detail, "Mac") {
			t.Fatalf("the Linux menu names a Mac: %+v", v)
		}
		if strings.Contains(v.Headline+v.Detail, "damaged") || v.Start.Enabled || v.Pause.Enabled || v.Pair.Enabled || !v.Quit.Enabled {
			t.Fatalf("the menu offers what an older app cannot do: %+v", v)
		}
	}
}

// The controller tells the menu which system it is on: beside a newer
// agent.json it cannot read, the menu of VaultSync for Mac names the app,
// the Linux menu-bar app names itself.
func TestIssue281_TheControllerSaysWhichMenuBarAppItIs(t *testing.T) {
	for _, mac := range []bool{true, false} {
		agent := newFakeAgent(t)
		if err := os.WriteFile(filepath.Join(agent.stateDir, "agent.json"), []byte(`{"version": 3, "guiPort": {"api": 8384}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		ui := newFakeUI(t)
		controllerFor(t, agent, ui, &Controller{Mac: mac})
		want := "This VaultSync menu-bar app is older than the VaultSync set up here"
		if mac {
			want = "This VaultSync for Mac is older than the one set up here"
		}
		ui.view(t, "older (Mac "+strconv.FormatBool(mac)+")", func(v View) bool { return v.Headline == want })
	}
}
