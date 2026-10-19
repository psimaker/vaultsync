package main

import (
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
	glyph    tray.Glyph
	hasIcon  bool
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
	m.pause = m.action("Pause Syncing", tray.EventPause)
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

// Render shows v. Safe to call from any goroutine (systray's items are).
func (m *menu) Render(v tray.View) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.hasIcon || m.glyph != v.Glyph {
		systray.SetTemplateIcon(tray.Icon(v.Glyph, true), tray.Icon(v.Glyph, false))
		m.glyph, m.hasIcon = v.Glyph, true
	}
	systray.SetTooltip("VaultSync — " + v.Headline)
	m.headline.SetTitle(v.Headline)
	setLine(m.detail, v.Detail, "")
	setLine(m.notice, v.Notice, "")
	fillPool(m.hubs, v.Hubs, false)
	setLine(m.noVault, v.NoVaultLine, "")
	lines := make([]tray.Line, len(v.Vaults))
	for i, vt := range v.Vaults {
		lines[i] = vt.Line
	}
	fillPool(m.vaults, lines, true)
	fillPool(m.waiting, v.Waiting, false)
	fillPool(m.attempts, v.Attempts, false)
	setAction(m.pause, v.Pause)
	setAction(m.pair, v.Pair)
	setAction(m.log, v.Log)
	setAction(m.quit, v.Quit)
}

// setLine shows text on it, or hides it when there is none.
func setLine(it *systray.MenuItem, text, tooltip string) {
	if text == "" {
		it.Hide()
		return
	}
	it.SetTitle(text)
	it.SetTooltip(tooltip)
	it.Show()
}

// fillPool shows the lines in the pool's slots and hides the rest; more
// lines than slots end in a last slot that says there are more. clickable
// pools get their lines enabled again — the "more" slot is disabled, and
// the slot keeps that until it is filled again.
func fillPool(pool []*systray.MenuItem, lines []tray.Line, clickable bool) {
	for i, it := range pool {
		switch {
		case i == len(pool)-1 && len(lines) > len(pool):
			setLine(it, "… and more; vaultsync status lists them all", "")
			it.Disable()
		case i < len(lines):
			setLine(it, lines[i].Text, lines[i].Tooltip)
			if clickable {
				it.Enable()
			}
		default:
			it.Hide()
		}
	}
}

func setAction(it *systray.MenuItem, a tray.Action) {
	if a.Title == "" {
		it.Hide()
		return
	}
	it.SetTitle(a.Title)
	it.Show()
	if a.Enabled {
		it.Enable()
	} else {
		it.Disable()
	}
}
