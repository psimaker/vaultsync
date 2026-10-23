package main

import (
	"reflect"
	"testing"

	"github.com/psimaker/vaultsync/desktop/tray"
)

// recorder is an item that remembers what was done to it.
type recorder struct {
	title, tooltip string
	shown, enabled bool
	calls          []string
}

func (r *recorder) SetTitle(s string)   { r.title = s; r.calls = append(r.calls, "title:"+s) }
func (r *recorder) SetTooltip(s string) { r.tooltip = s; r.calls = append(r.calls, "tooltip:"+s) }
func (r *recorder) Show()               { r.shown = true; r.calls = append(r.calls, "show") }
func (r *recorder) Hide()               { r.shown = false; r.calls = append(r.calls, "hide") }
func (r *recorder) Enable()             { r.enabled = true; r.calls = append(r.calls, "enable") }
func (r *recorder) Disable()            { r.enabled = false; r.calls = append(r.calls, "disable") }

func recorders(n int) ([]item, []*recorder) {
	pool := make([]item, n)
	recs := make([]*recorder, n)
	for i := range pool {
		recs[i] = &recorder{enabled: true}
		pool[i] = recs[i]
	}
	return pool, recs
}

func lines(texts ...string) []tray.Line {
	out := make([]tray.Line, len(texts))
	for i, t := range texts {
		out[i] = tray.Line{Text: t, Tooltip: "tip " + t}
	}
	return out
}

func TestIssue176_MenuSlotsShowTheirLinesAndNothingElse(t *testing.T) {
	pool, recs := recorders(3)
	// Two lines in three slots: the third hidden.
	fillPool(pool, true, nil, lines("a", "b"), true)
	if recs[0].title != "a" || recs[0].tooltip != "tip a" || !recs[0].shown || !recs[0].enabled || recs[1].title != "b" || !recs[1].shown || recs[2].shown {
		t.Fatalf("two lines: %+v %+v %+v", recs[0], recs[1], recs[2])
	}
	// Nothing changed: nothing touched (a poll that found nothing new
	// sends the desktop's menu host nothing).
	for _, r := range recs {
		r.calls = nil
	}
	fillPool(pool, false, lines("a", "b"), lines("a", "b"), true)
	for i, r := range recs {
		if len(r.calls) != 0 {
			t.Fatalf("slot %d touched for an unchanged line: %v", i, r.calls)
		}
	}
	// One line changed: that slot alone.
	fillPool(pool, false, lines("a", "b"), lines("a", "c"), true)
	if len(recs[0].calls) != 0 || recs[1].title != "c" || len(recs[2].calls) != 0 {
		t.Fatalf("one changed line: %v / %v / %v", recs[0].calls, recs[1].calls, recs[2].calls)
	}
	// More lines than slots: the last slot is the "more" line, disabled;
	// fewer again: that slot shows a line and is clickable again.
	fillPool(pool, false, lines("a", "c"), lines("a", "c", "d", "e"), true)
	if recs[2].title != moreLine.Text || !recs[2].shown || recs[2].enabled {
		t.Fatalf("the more line: %+v", recs[2])
	}
	fillPool(pool, false, lines("a", "c", "d", "e"), lines("a", "c", "d"), true)
	if recs[2].title != "d" || !recs[2].shown || !recs[2].enabled {
		t.Fatalf("a slot after serving as the more line: %+v", recs[2])
	}
	// Exactly as many lines as slots: all shown, no more line.
	fillPool(pool, true, nil, lines("x", "y", "z"), true)
	if recs[2].title != "z" || !recs[2].enabled {
		t.Fatalf("a full pool: %+v", recs[2])
	}
	// A plain pool (hubs, waiting) never enables its lines.
	plain, plainRecs := recorders(2)
	for _, r := range plainRecs {
		r.enabled = false
	}
	fillPool(plain, true, nil, lines("h"), false)
	if plainRecs[0].enabled || !plainRecs[0].shown || plainRecs[1].shown {
		t.Fatalf("a plain pool: %+v %+v", plainRecs[0], plainRecs[1])
	}
}

func TestIssue176_MenuLinesAndActionsChangeOnlyWhenTheyDiffer(t *testing.T) {
	r := &recorder{}
	setLine(r, true, tray.Line{}, tray.Line{Text: "t", Tooltip: "tip"})
	if r.title != "t" || r.tooltip != "tip" || !r.shown {
		t.Fatalf("a line: %+v", r)
	}
	r.calls = nil
	setLine(r, false, tray.Line{Text: "t", Tooltip: "tip"}, tray.Line{Text: "t", Tooltip: "tip"})
	if len(r.calls) != 0 {
		t.Fatalf("an unchanged line touched: %v", r.calls)
	}
	setLine(r, false, tray.Line{Text: "t", Tooltip: "tip"}, tray.Line{})
	if r.shown || !reflect.DeepEqual(r.calls, []string{"hide"}) {
		t.Fatalf("an emptied line: %+v", r)
	}

	a := &recorder{}
	setAction(a, true, tray.Action{}, tray.Action{Title: "Pause Syncing", Enabled: true})
	if a.title != "Pause Syncing" || !a.shown || !a.enabled {
		t.Fatalf("an action: %+v", a)
	}
	a.calls = nil
	setAction(a, false, tray.Action{Title: "Pause Syncing", Enabled: true}, tray.Action{Title: "Pause Syncing", Enabled: true})
	if len(a.calls) != 0 {
		t.Fatalf("an unchanged action touched: %v", a.calls)
	}
	setAction(a, false, tray.Action{Title: "Pause Syncing", Enabled: true}, tray.Action{Title: "Resume Syncing", Enabled: false})
	if a.title != "Resume Syncing" || a.enabled {
		t.Fatalf("a changed action: %+v", a)
	}
	setAction(a, false, tray.Action{Title: "Resume Syncing"}, tray.Action{})
	if a.shown {
		t.Fatalf("an action without a title stays shown: %+v", a)
	}
}
