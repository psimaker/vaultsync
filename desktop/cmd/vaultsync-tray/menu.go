package main

import (
	"reflect"
	"sync"

	"fyne.io/systray"
	"github.com/psimaker/vaultsync/desktop/tray"
)

// menu is the systray side of tray.UI: a fixed set of items made once
// (systray appends, so the order is set at build), with pools of lines
// that are shown, retitled or hidden as the view changes.

const (
	hubSlots     = 8
	vaultSlots   = 24
	waitingSlots = 8
	attemptSlots = 8
)

type menu struct {
	events chan tray.Event
	// onQuit runs on a Quit click before the event is sent — it cancels
	// the controller directly, so a Quit is never lost in a full buffer
	// while the controller waits on the agent.
	onQuit func()

	mu       sync.Mutex
	headline *systray.MenuItem
	detail   *systray.MenuItem
	notice   *systray.MenuItem
	hubs     []*systray.MenuItem
	noVault  *systray.MenuItem
	vaults   []*systray.MenuItem
	waiting  []*systray.MenuItem
	attempts []*systray.MenuItem
	pause    *systray.MenuItem
	pair     *systray.MenuItem
	log      *systray.MenuItem
	quit     *systray.MenuItem
	// last is the view as rendered; only what differs from it is touched
	// again (each change is a signal to the desktop's menu host, and a
	// poll that found nothing new should send none).
	last     tray.View
	rendered bool
}

func newMenu() *menu {
	return &menu{events: make(chan tray.Event, 8)}
}

func (m *menu) Events() <-chan tray.Event { return m.events }

// build makes the items; called once systray is ready.
func (m *menu) build() {
	m.mu.Lock()
	defer m.mu.Unlock()
	systray.SetTooltip("VaultSync")
	m.headline = disabledItem("VaultSync")
	m.detail = disabledItem("")
	m.notice = disabledItem("")
	m.hubs = m.pool(hubSlots, nil)
	systray.AddSeparator()
	m.noVault = disabledItem("No vault yet")
	m.vaults = m.pool(vaultSlots, func(i int) tray.Event { return tray.Event{Kind: tray.EventOpenVault, Index: i} })
	m.waiting = m.pool(waitingSlots, nil)
	m.attempts = m.pool(attemptSlots, nil)
	systray.AddSeparator()
	m.pause = systray.AddMenuItem("Pause Syncing", "")
	go func() {
		for range m.pause.ClickedCh {
			// What the menu showed at the click: pause when it offered to
			// pause, resume when it offered to resume.
			m.mu.Lock()
			pause := !m.last.Paused
			m.mu.Unlock()
			m.send(tray.Event{Kind: tray.EventPause, Pause: pause})
		}
	}()
	m.pair = m.action("Pair a Vault…", tray.EventPair)
	m.log = m.action("Open Log", tray.EventOpenLog)
	systray.AddSeparator()
	m.quit = systray.AddMenuItem("Quit", "")
	go func() {
		for range m.quit.ClickedCh {
			if m.onQuit != nil {
				m.onQuit()
			}
			m.send(tray.Event{Kind: tray.EventQuit})
		}
	}()
	go func() {
		for range systray.TrayOpenedCh {
			m.send(tray.Event{Kind: tray.EventOpened})
		}
	}()
}

// send hands an event to the controller without ever blocking the UI
// thread: a click while the controller is busy is dropped, not queued
// behind it.
func (m *menu) send(ev tray.Event) {
	select {
	case m.events <- ev:
	default:
	}
}

func disabledItem(title string) *systray.MenuItem {
	it := systray.AddMenuItem(title, "")
	it.Disable()
	if title == "" {
		it.Hide()
	}
	return it
}

// pool makes n hidden items; with an event maker each click sends the
// event for its slot, else the items are plain lines.
func (m *menu) pool(n int, event func(i int) tray.Event) []*systray.MenuItem {
	items := make([]*systray.MenuItem, n)
	for i := range items {
		it := systray.AddMenuItem("", "")
		it.Hide()
		if event == nil {
			it.Disable()
		} else {
			go func(i int, it *systray.MenuItem) {
				for range it.ClickedCh {
					m.send(event(i))
				}
			}(i, it)
		}
		items[i] = it
	}
	return items
}

func (m *menu) action(title string, kind tray.EventKind) *systray.MenuItem {
	it := systray.AddMenuItem(title, "")
	go func() {
		for range it.ClickedCh {
			m.send(tray.Event{Kind: kind})
		}
	}()
	return it
}

// Render shows v: on the first call everything, afterwards only what
// differs from the view last rendered. Safe to call from any goroutine
// (systray's items are).
func (m *menu) Render(v tray.View) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rendered && reflect.DeepEqual(v, m.last) {
		return
	}
	first := !m.rendered
	last := m.last
	if first || v.Glyph != last.Glyph {
		systray.SetTemplateIcon(tray.Icon(v.Glyph, true), tray.Icon(v.Glyph, false))
	}
	if first || v.Headline != last.Headline {
		systray.SetTooltip("VaultSync — " + v.Headline)
		m.headline.SetTitle(v.Headline)
	}
	setLine(m.detail, first, tray.Line{Text: last.Detail}, tray.Line{Text: v.Detail})
	setLine(m.notice, first, tray.Line{Text: last.Notice}, tray.Line{Text: v.Notice})
	fillPool(m.hubs, first, last.Hubs, v.Hubs, false)
	setLine(m.noVault, first, tray.Line{Text: last.NoVaultLine}, tray.Line{Text: v.NoVaultLine})
	fillPool(m.vaults, first, vaultLines(last.Vaults), vaultLines(v.Vaults), true)
	fillPool(m.waiting, first, last.Waiting, v.Waiting, false)
	fillPool(m.attempts, first, last.Attempts, v.Attempts, false)
	setAction(m.pause, first, last.Pause, v.Pause)
	setAction(m.pair, first, last.Pair, v.Pair)
	setAction(m.log, first, last.Log, v.Log)
	setAction(m.quit, first, last.Quit, v.Quit)
	m.last, m.rendered = v, true
}

func vaultLines(vs []tray.VaultLine) []tray.Line {
	lines := make([]tray.Line, len(vs))
	for i, vt := range vs {
		lines[i] = vt.Line
	}
	return lines
}

// setLine shows the line on it, or hides it when there is none — only
// when it differs from what the item shows.
func setLine(it *systray.MenuItem, force bool, was, now tray.Line) {
	if !force && was == now {
		return
	}
	if now.Text == "" {
		it.Hide()
		return
	}
	it.SetTitle(now.Text)
	it.SetTooltip(now.Tooltip)
	it.Show()
}

// moreLine stands in the last slot when there are more lines than slots.
var moreLine = tray.Line{Text: "… and more; vaultsync status lists them all"}

// slotLine is what slot i of a pool shows for lines: the line, the
// "more" line in the last slot, or nothing.
func slotLine(pool []*systray.MenuItem, lines []tray.Line, i int) (line tray.Line, more bool) {
	switch {
	case i == len(pool)-1 && len(lines) > len(pool):
		return moreLine, true
	case i < len(lines):
		return lines[i], false
	}
	return tray.Line{}, false
}

// fillPool shows the lines in the pool's slots and hides the rest; more
// lines than slots end in a last slot that says there are more. Clickable
// pools get their lines enabled again after the "more" slot, which is
// disabled. Only slots whose line changed are touched.
func fillPool(pool []*systray.MenuItem, force bool, was, now []tray.Line, clickable bool) {
	for i, it := range pool {
		before, wasMore := slotLine(pool, was, i)
		line, more := slotLine(pool, now, i)
		if !force && before == line && wasMore == more {
			continue
		}
		setLine(it, true, before, line)
		if line.Text == "" {
			continue
		}
		switch {
		case more:
			it.Disable()
		case clickable:
			it.Enable()
		}
	}
}

func setAction(it *systray.MenuItem, force bool, was, now tray.Action) {
	if !force && was == now {
		return
	}
	if now.Title == "" {
		it.Hide()
		return
	}
	it.SetTitle(now.Title)
	it.Show()
	if now.Enabled {
		it.Enable()
	} else {
		it.Disable()
	}
}
