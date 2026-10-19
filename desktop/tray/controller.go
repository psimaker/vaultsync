package tray

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/psimaker/vaultsync/desktop/internal/control"
	"github.com/psimaker/vaultsync/desktop/internal/layout"
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
	// Path, for EventOpenVault, is the folder the clicked line showed: a
	// refresh between the click and its handling must not open another
	// vault's folder that moved into the slot.
	Path string
	// Pause is what an EventPause asked for — true to pause, false to
	// resume — as the menu showed it at the click, so a click queued
	// behind a slow request never undoes what it asked for.
	Pause bool
}

// UI is the menu bar: it shows a View and reports what was clicked.
type UI interface {
	Render(View)
	Events() <-chan Event
}

// Controller runs the loop. The fields are the computer and the programs
// the loop runs; tests give it fakes.
type Controller struct {
	Home string
	// Layout is where the agent keeps its files on this computer.
	Layout layout.Layout
	UI     UI
	// Asker shows the pairing dialogs; nil when this computer cannot
	// (the Pair item then says to use a terminal).
	Asker Asker
	// Open opens a folder or a file with the desktop.
	Open func(ctx context.Context, path string) error
	// Notify shows a short notification, or nothing (nil).
	Notify func(ctx context.Context, text string)
	// Poll is how often the agent is asked between opens; 0 is 30 s.
	Poll time.Duration
	// Logf writes a line to the log (stderr); nil discards.
	Logf func(string, ...any)
	// StatusTimeout bounds one status request; 0 is 10 s.
	StatusTimeout time.Duration
	// Lock takes the one-instance lock (TakeLock, with the layout's Base);
	// Held is one the caller already holds. Not held — VaultSync's folder was not there at start
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
	// lockWords: why the one-instance lock could not be taken, shown next
	// to the notice until it holds; lockSaid: the failure is in the log.
	lockWords string
	lockSaid  bool
	cl        *Client
}

// LockHandle is a held one-instance lock (*Lock).
type LockHandle interface {
	Valid() bool
	Release()
}

const (
	// defaultPoll is how often the agent is asked while nobody looks: a
	// full report costs the engine a request per vault and Hub, so the
	// icon may lag by half a minute; opening the menu asks at once.
	defaultPoll    = 30 * time.Second
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
	openFailed := make(chan string, 8)
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
		case notice := <-openFailed:
			c.notice = notice
			c.notify(ctx, notice)
			c.render()
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
				c.openVault(ev.Path, openFailed)
			case EventOpenLog:
				c.openLog(openFailed)
			case EventPause:
				err = c.setPaused(ctx, ev.Pause)
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
// held one no longer guards the file at its place. A second instance is
// the error that ends Run; any other failure to take it is said in the
// menu and tried again at the next refresh.
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
	held, err := c.Lock(c.Layout.Base)
	if errors.Is(err, ErrAlreadyRunning) {
		return err
	}
	if err != nil {
		// The words for the menu; the log gets the fact alone (the error
		// names VaultSync's folder), once per failure.
		c.lockWords = "VaultSync's folder could not be locked for this menu-bar app (" + err.Error() + "); trying again"
		if !c.lockSaid {
			c.logf("one-instance lock: could not be taken; trying again at every refresh")
			c.lockSaid = true
		}
		return nil
	}
	c.lockWords, c.lockSaid = "", false
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
	if ctx.Err() != nil {
		// Quit, or the system ending the app: a request cut short is not
		// a state to show on the way out.
		return ctx.Err()
	}
	if errors.Is(err, ErrNoAgent) && control.SocketErrorRecorded(c.Layout) {
		err = ErrNoSocket
	}
	if errors.Is(err, context.DeadlineExceeded) {
		err = errNoAnswer
	}
	c.status, c.known, c.lastErr = st, err == nil, err
	c.render()
	return nil
}

// render shows the last refresh's outcome again, with whatever changed
// since (a notice, a pairing under way).
func (c *Controller) render() {
	notice := c.notice
	if c.lockWords != "" {
		// The lock's trouble stands beside what the last action said,
		// never in its place.
		if notice != "" {
			notice += " · "
		}
		notice += c.lockWords
	}
	c.view = Render(c.status, c.lastErr, Options{
		Home:    c.Home,
		LogPath: c.logFile(),
		Pairing: c.pairing,
		CanAsk:  c.Asker != nil,
		Notice:  notice,
	})
	c.UI.Render(c.view)
}

// client is the agent as agent.json names it right now: one client, and
// its one connection, for as long as the socket's place stays the same.
func (c *Controller) client() (*Client, error) {
	path, err := control.SocketPath(c.Layout)
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

// openVault opens the folder the clicked line showed — one the agent
// listed, now or at the click: a path that is no vault's is not opened.
func (c *Controller) openVault(path string, failed chan<- string) {
	if path == "" {
		return
	}
	for _, vt := range c.view.Vaults {
		if vt.Path == path {
			c.openPath(path, failed)
			return
		}
	}
}

func (c *Controller) openLog(failed chan<- string) {
	if path := c.logFile(); path != "" {
		c.openPath(path, failed)
	}
}

// logFile is the service's log when there is one to open: an agent run
// from a terminal, or one never installed, has none.
func (c *Controller) logFile() string {
	path := c.Layout.LogFile()
	if path == "" {
		return ""
	}
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

// openPath opens path off the loop: the desktop's program may not return
// before what it opened is closed, and the menu must not wait for that.
// A failure comes back as a notice.
func (c *Controller) openPath(path string, failed chan<- string) {
	if c.Open == nil {
		return
	}
	go func() {
		if err := c.Open(context.Background(), path); err != nil {
			select {
			case failed <- "Could not open it: " + err.Error():
			default:
			}
		}
	}()
}

// setPaused pauses or resumes as the click asked; a click that asks for
// the state already in place (one queued behind a slow request, or
// made on a stale menu) does nothing. The agent is the one that pauses.
func (c *Controller) setPaused(ctx context.Context, pause bool) error {
	if !c.known || !c.view.Pause.Enabled || pause == c.status.Paused {
		return nil
	}
	cl, err := c.client()
	var res PauseResult
	if err == nil {
		pctx, cancel := context.WithTimeout(ctx, pauseTimeout)
		res, err = cl.SetPaused(pctx, pause)
		cancel()
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		c.notice = errNoAnswer.Error()
		c.notify(ctx, c.notice)
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
		line, err := Pair(pctx, cl, c.Asker)
		if err != nil {
			// The fact for the log; the words — which may name a vault or
			// a folder — go to the menu's notice and the dialog.
			c.logf("pairing: ended with an error; the menu says which")
		}
		line = pairingOutcome(line, err)
		// The time limit is the one end no dialog told the person about;
		// every other failure was shown as it happened.
		if errors.Is(err, context.DeadlineExceeded) {
			c.notify(ctx, line)
		}
		done <- line
	}()
}

// errNoAnswer: the agent took longer than the request's limit — busy, or
// stuck; the next refresh asks again.
var errNoAnswer = errors.New("VaultSync did not answer in time; it may be busy, and is asked again")

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
