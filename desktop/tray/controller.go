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
	// Lock takes the one-instance lock (TakeLock); Held is one the caller
	// already holds. Not held — VaultSync's folder was not there at start
	// — it is tried again at every refresh, so a second instance started
	// before setup ends once setup made the folder; one that stopped
	// being valid (the file taken away) is released and taken again.
	Lock func(stateDir string) (LockHandle, error)
	Held LockHandle

	status  Status
	known   bool  // status came from the agent
	lastErr error // why there is no status, for every render until the next refresh
	view    View
	pairing bool
	notice  string
	cl      *Client
}

// LockHandle is a held one-instance lock (*Lock).
type LockHandle interface {
	Valid() bool
	Release()
}

const (
	defaultPoll    = 10 * time.Second
	defaultTimeout = 10 * time.Second
	pauseTimeout   = 15 * time.Second
	// pairTimeout is as long as the agent itself allows a pairing to take.
	pairTimeout = 15 * time.Minute
)

// Run serves the menu until the person quits or ctx ends. It ends with
// an error when a second instance is found running (the lock).
func (c *Controller) Run(ctx context.Context) error {
	poll := c.Poll
	if poll <= 0 {
		poll = defaultPoll
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	defer func() {
		if c.Held != nil {
			c.Held.Release()
		}
		if c.cl != nil {
			c.cl.CloseIdleConnections()
		}
	}()
	pairDone := make(chan string, 1)
	if err := c.refresh(ctx); err != nil {
		return err
	}
	for {
		var err error
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			err = c.refresh(ctx)
		case line := <-pairDone:
			c.pairing = false
			c.notice = line
			err = c.refresh(ctx)
		case ev, ok := <-c.UI.Events():
			if !ok {
				return nil
			}
			switch ev.Kind {
			case EventQuit:
				return nil
			case EventOpened:
				err = c.refresh(ctx)
			case EventOpenVault:
				c.openVault(ctx, ev.Index)
			case EventOpenLog:
				c.openLog(ctx)
			case EventPause:
				err = c.togglePause(ctx)
			case EventPair:
				c.startPairing(ctx, pairDone)
			}
		}
		if err != nil {
			return err
		}
	}
}

// takeLock tries the one-instance lock when none is held, or when the
// held one no longer guards the file at its place; a second instance is
// the error that ends Run.
func (c *Controller) takeLock() error {
	if c.Lock == nil {
		return nil
	}
	if c.Held != nil && !c.Held.Valid() {
		c.Held.Release()
		c.Held = nil
	}
	if c.Held != nil {
		return nil
	}
	held, err := c.Lock(c.StateDir)
	if err != nil {
		return err
	}
	if held != nil {
		c.Held = held
	}
	return nil
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
func (c *Controller) refresh(ctx context.Context) error {
	if err := c.takeLock(); err != nil {
		return err
	}
	timeout := c.StatusTimeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	var st Status
	cl, err := c.client()
	if err == nil {
		sctx, cancel := context.WithTimeout(ctx, timeout)
		st, err = cl.Status(sctx)
		cancel()
	}
	if errors.Is(err, ErrNoAgent) && SocketErrorRecorded(c.StateDir) {
		err = ErrNoSocket
	}
	c.status, c.known, c.lastErr = st, err == nil, err
	c.render()
	return nil
}

// render shows the last refresh's outcome again, with whatever changed
// since (a notice, a pairing under way).
func (c *Controller) render() {
	c.view = Render(c.status, c.lastErr, Options{
		Home:    c.Home,
		LogPath: LogPath(c.GOOS, c.Home),
		Pairing: c.pairing,
		CanAsk:  c.Asker != nil,
		Notice:  c.notice,
	})
	c.UI.Render(c.view)
}

// client is the agent as agent.json names it right now: one client, and
// its one connection, for as long as the socket's place stays the same.
func (c *Controller) client() (*Client, error) {
	path, err := SocketPath(c.StateDir)
	if err != nil {
		return nil, err
	}
	if c.cl == nil || c.cl.Path() != path {
		if c.cl != nil {
			c.cl.CloseIdleConnections()
		}
		c.cl = NewClient(path)
	}
	return c.cl, nil
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
		c.render()
	}
}

// togglePause pauses when syncing runs, resumes when it is paused — as
// the view last showed it; the agent is the one that pauses.
func (c *Controller) togglePause(ctx context.Context) error {
	if !c.known || !c.view.Pause.Enabled {
		return nil
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
	return c.refresh(ctx)
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
		c.render()
		return
	}
	c.pairing = true
	c.notice = ""
	c.render()
	go func() {
		pctx, cancel := context.WithTimeout(ctx, pairTimeout)
		defer cancel()
		line, err := Pair(pctx, cl, c.Asker, c.Home)
		if err != nil {
			c.logf("pairing: %v", err)
		}
		line = pairingOutcome(line, err)
		if err != nil && line != "" {
			c.notify(ctx, line)
		}
		done <- line
	}()
}

// pairingTimeoutNotice says what a pairing that ran out of time leaves
// behind: the agent may still finish what it had taken on.
const pairingTimeoutNotice = "The pairing was stopped after 15 minutes without an answer; vaultsync status shows what came of it, and Pair a Vault… starts over"

// pairingOutcome is the notice a pairing ends with: what it said, or why
// it ended — nothing for a quit, the time limit in its own words.
func pairingOutcome(line string, err error) string {
	switch {
	case err == nil:
		return line
	case errors.Is(err, context.Canceled):
		return ""
	case errors.Is(err, context.DeadlineExceeded):
		return pairingTimeoutNotice
	}
	return err.Error()
}
