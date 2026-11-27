package main

import (
	"strings"
	"testing"

	"github.com/psimaker/vaultsync/desktop/tray"
)

// #291: a notice, and the line under the headline, take a few short lines of
// the menu instead of one that macOS cuts in its middle.
func TestIssue291_ATextTakesAFewLinesOfTheMenu(t *testing.T) {
	pool, recs := recorders(tray.MenuLinesMost)
	for _, r := range recs {
		r.enabled = false // as pool makes a plain line: never clickable
	}
	long := "VaultSync's background service runs, but it does not answer yet — it may still be installing its sync engine. vaultsync status says what it does. If it stays so, run vaultsync stop, then vaultsync start; where that changes nothing, run the setup link, which brings the current VaultSync to the service (its log: ~/Library/Logs/VaultSync/vaultsync.log)."
	fillText(pool, true, "", long)
	for i, r := range recs {
		if !r.shown || r.title == "" || len([]rune(r.title)) > tray.MenuLineWidth || r.enabled || !strings.Contains(r.tooltip, "vaultsync stop, then vaultsync start") {
			t.Fatalf("slot %d: %+v", i, r)
		}
		if r.title == "… and more; vaultsync status lists them all" {
			t.Fatalf("slot %d shows the list's more line", i)
		}
	}
	if !strings.Contains(recs[len(recs)-1].title, "vaultsync.log") {
		t.Fatalf("the end: %+v", recs[len(recs)-1])
	}
	// A short one: one line, the rest hidden; none: all hidden.
	fillText(pool, false, long, "Nothing was changed.")
	if recs[0].title != "Nothing was changed." || recs[0].tooltip != "" || !recs[0].shown || recs[1].shown || recs[2].shown || recs[3].shown {
		t.Fatalf("a short text: %+v", recs)
	}
	fillText(pool, false, "Nothing was changed.", "")
	for i, r := range recs {
		if r.shown {
			t.Fatalf("slot %d shown for no text: %+v", i, r)
		}
	}
}
