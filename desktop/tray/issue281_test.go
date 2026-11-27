package tray

import (
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
