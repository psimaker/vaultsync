package tray

import (
	"strings"
	"testing"
)

// #281: beside an agent.json in a newer format that it cannot read, the
// menu says this VaultSync for Mac is older and how to update it — never
// that the file is damaged, which a person may delete.
func TestIssue281_TheMenuSaysThisAppIsOlder(t *testing.T) {
	v := Render(Status{}, ErrOlderApp, Options{CanStart: true})
	if v.Glyph != GlyphAttention || v.Headline != "This VaultSync for Mac is older than the one set up here" || v.Detail != "Run the setup link to update it" {
		t.Fatalf("the menu beside a newer agent.json: %+v", v)
	}
	if strings.Contains(v.Headline+v.Detail, "damaged") || v.Start.Enabled || v.Pause.Enabled || v.Pair.Enabled || !v.Quit.Enabled {
		t.Fatalf("the menu offers what an older app cannot do: %+v", v)
	}
}
