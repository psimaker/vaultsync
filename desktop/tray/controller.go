package tray

import (
	"context"
	"errors"
	"time"
)

// The loop that keeps the menu and the agent together: it asks the agent
// for its status every few seconds and when the menu opens, renders what
// it got, and turns a click into the one request the terminal would make
// for it — pause, resume, pair, open a folder.

type EventKind int

const (
	// EventOpened: the menu was opened — refresh before the person reads
	// it (the icon alone may be a few seconds old).
	EventOpened EventKind = iota
	// EventOpenVault: Index is the vault's position in the view.
	EventOpenVault
	// EventPause: pause or resume, whichever the view offered.
	EventPause
	EventPair
	EventOpenLog
	EventQuit
)

type Event struct {
	Kind  EventKind
	Index int
}

// UI is the menu bar: it shows a View and reports what was clicked.
type UI interface {
	Render(View)
	Events() <-chan Event
}

// Controller runs the loop. The fields are the computer and the programs
// the loop runs; tests give it fakes.
type Controller struct {
	GOOS, Home string
	StateDir   string
	UI         UI
	// Asker shows the pairing dialogs; nil when this computer cannot
	// (the Pair item then says to use a terminal).
	Asker Asker
	// Open opens a folder or a file with the desktop.
	Open func(ctx context.Context, path string) error
	// Notify shows a short notification, or nothing (nil).
	Notify func(ctx context.Context, text string)
	// Poll is how often the agent is asked between opens; 0 is 10 s.
	Poll time.Duration
	// Logf writes a line to the log (stderr); nil discards.
	Logf func(string, ...any)
	// StatusTimeout bounds one status request; 0 is 10 s.
	StatusTimeout time.Duration

	status  Status
	known   bool // status came from the agent
	view    View
	pairing bool
	notice  string
}

const (
	defaultPoll    = 10 * time.Second
	defaultTimeout = 10 * time.Second
	pauseTimeout   = 15 * time.Second
	// pairTimeout is as long as the agent itself allows a pairing to take.
	pairTimeout = 15 * time.Minute
)

// Run serves the menu until the person quits or ctx ends.
func (c *Controller) Run(ctx context.Context) error {
	poll := c.Poll
	if poll <= 0 {
		poll = defaultPoll
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	pairDone := make(chan string, 1)
	c.refresh(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			c.refresh(ctx)
		case line := <-pairDone:
			c.pairing = false
			c.notice = line
			c.refresh(ctx)
		case ev, ok := <-c.UI.Events():
			if !ok {
				return nil
			}
			switch ev.Kind {
			case EventQuit:
				return nil
			case EventOpened:
				c.refresh(ctx)
			case EventOpenVault:
				c.openVault(ctx, ev.Index)
			case EventOpenLog:
				c.openLog(ctx)
			case EventPause:
				c.togglePause(ctx)
			case EventPair:
				c.startPairing(ctx, pairDone)
			}
		}
	}
}

func (c *Controller) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
	}
}

func (c *Controller) notify(ctx context.Context, text string) {
	if c.Notify != nil {
		c.Notify(ctx, text)
	}
}

// refresh asks the agent and renders the answer, or why there is none.
func (c *Controller) refresh(ctx context.Context) {
	timeout := c.StatusTimeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	var st Status
	path, err := SocketPath(c.StateDir)
	if err == nil {
		sctx, cancel := context.WithTimeout(ctx, timeout)
		st, err = NewClient(path).Status(sctx)
		cancel()
	}
	c.status, c.known = st, err == nil
	c.render(err)
}

func (c *Controller) render(err error) {
	c.view = Render(c.status, err, Options{
		Home:    c.Home,
		LogPath: LogPath(c.GOOS, c.Home),
		Pairing: c.pairing,
		CanAsk:  c.Asker != nil,
		Notice:  c.notice,
	})
	c.UI.Render(c.view)
}

// client is the agent as agent.json names it right now.
func (c *Controller) client() (*Client, error) {
	path, err := SocketPath(c.StateDir)
	if err != nil {
		return nil, err
	}
	return NewClient(path), nil
}

func (c *Controller) openVault(ctx context.Context, i int) {
	if i < 0 || i >= len(c.view.Vaults) || c.view.Vaults[i].Path == "" {
		return
	}
	c.openPath(ctx, c.view.Vaults[i].Path)
}

func (c *Controller) openLog(ctx context.Context) {
	if path := LogPath(c.GOOS, c.Home); path != "" {
		c.openPath(ctx, path)
	}
}

func (c *Controller) openPath(ctx context.Context, path string) {
	if c.Open == nil {
		return
	}
	if err := c.Open(ctx, path); err != nil {
		c.notice = "Could not open it: " + err.Error()
		c.notify(ctx, c.notice)
		c.render(nil)
	}
}

// togglePause pauses when syncing runs, resumes when it is paused — as
// the view last showed it; the agent is the one that pauses.
func (c *Controller) togglePause(ctx context.Context) {
	if !c.known || !c.view.Pause.Enabled {
		return
	}
	pause := !c.status.Paused
	cl, err := c.client()
	var res PauseResult
	if err == nil {
		pctx, cancel := context.WithTimeout(ctx, pauseTimeout)
		res, err = cl.SetPaused(pctx, pause)
		cancel()
	}
	switch {
	case err != nil:
		c.notice = err.Error()
		c.notify(ctx, c.notice)
	case res.Hubs == 0 && pause:
		c.notice = "No Hub is paired with this computer yet, so there is nothing to pause."
		c.notify(ctx, c.notice)
	case res.Hubs == 0:
		c.notice = "No Hub is paired with this computer yet, so there is nothing to resume."
		c.notify(ctx, c.notice)
	default:
		c.notice = ""
	}
	c.refresh(ctx)
}

// startPairing runs the conversation in the background: a pairing takes
// minutes, and the menu keeps showing the agent's status meanwhile. The
// agent runs the pairing on its own lifetime, so quitting the menu-bar
// app mid-way does not abort an accept (decision 049).
func (c *Controller) startPairing(ctx context.Context, done chan<- string) {
	if c.pairing || c.Asker == nil || !c.view.Pair.Enabled {
		return
	}
	cl, err := c.client()
	if err != nil {
		c.notice = err.Error()
		c.notify(ctx, c.notice)
		c.render(nil)
		return
	}
	c.pairing = true
	c.notice = ""
	c.render(nil)
	go func() {
		pctx, cancel := context.WithTimeout(ctx, pairTimeout)
		defer cancel()
		line, err := Pair(pctx, cl, c.Asker, c.Home)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				line = ""
			} else {
				c.logf("pairing: %v", err)
				line = err.Error()
			}
		}
		done <- line
	}()
}
