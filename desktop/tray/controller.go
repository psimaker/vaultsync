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
	// EventStart: start the background service, as vaultsync start does.
	EventStart
)

type Event struct {
	Kind EventKind
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
	// StartProgram says which vaultsync the Start item runs, "" for none;
	// Start runs it and returns what it said, or why it refused. Without
	// them the menu points to a terminal.
	StartProgram func() string
	Start        func(ctx context.Context, program string) (string, error)
	// StartWaits replaces the seconds between asking the agent again after
	// a start; a test shortens them.
	StartWaits []time.Duration
	// Notify shows a short notification, or nothing (nil).
	Notify func(ctx context.Context, text string)
	// Poll is how often the agent is asked between opens; 0 is 30 s.
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
	Lock func(lay layout.Layout) (LockHandle, error)
	Held LockHandle

	status  Status
	known   bool  // status came from the agent
	lastErr error // why there is no status, for every render until the next refresh
	view    View
	pairing bool
	notice  string
	// openNotice: the notice an open's failure set, for openNoticePath —
	// the one an open of that path that works takes back (it may ask to
	// click the vault again).
	openNotice     string
	openNoticePath string
	// starting: a start from the menu is at it. startNotice: the notice it
	// left, which goes once an agent answers and speaks for itself.
	starting bool
	// settling: vaultsync start has said the service runs and its asks have
	// not ended. A click now would restart a service on its way up.
	settling    bool
	startNotice string
	canStart    bool
	// lockWords: why the one-instance lock could not be taken, shown next
	// to the notice until it holds; lockSaid: the failure is in the log.
	lockWords string
	lockSaid  bool
	cl        *Client
	// logPath is the service's log when there is one to open, looked at
	// once per refresh.
	logPath string
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
	openDone := make(chan openOutcome, 8)
	startDone := make(chan startOutcome, 1)
	// The agent is asked again in the seconds after a start: the service
	// runs by then, but its socket comes a moment later, its engine after
	// that, and the next poll is up to half a minute away. nil when no start
	// is being followed.
	var askAgain <-chan time.Time
	var waits []time.Duration
	var waitEnds time.Time
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
		case out := <-startDone:
			c.starting = false
			c.notice, c.startNotice = out.notice, out.notice
			if out.failed {
				c.notify(ctx, out.notice)
			} else {
				c.settling = true
				waits = c.startWaits()
				askAgain = time.After(waits[0])
				waitEnds = time.Now()
				for _, wait := range waits {
					waitEnds = waitEnds.Add(wait)
				}
			}
			err = c.refresh(ctx)
		case <-askAgain:
			waits = waits[1:]
			askAgain = nil
			if err = c.refresh(ctx); err != nil {
				break
			}
			// By the clock too: a socket that stalls takes a status timeout
			// per ask, on this loop.
			if len(waits) > 0 && time.Now().Before(waitEnds) {
				askAgain = time.After(waits[0])
			} else {
				c.endOfWait(ctx)
			}
		case line := <-pairDone:
			c.pairing = false
			c.notice = line
			err = c.refresh(ctx)
		case out := <-openDone:
			switch {
			case out.notice != "":
				c.notice, c.openNotice, c.openNoticePath = out.notice, out.notice, out.path
				c.notify(ctx, out.notice)
				c.render()
			case c.notice != "" && c.notice == c.openNotice && out.path == c.openNoticePath:
				c.notice = ""
				c.render()
			}
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
				c.openVault(ctx, ev.Path, openDone)
			case EventOpenLog:
				c.openLog(ctx, openDone)
			case EventStart:
				err = c.startService(ctx, startDone)
			case EventPause:
				err = c.setPaused(ctx, ev.Pause)
			case EventPair:
				err = c.startPairing(ctx, pairDone)
			}
		}
		if err != nil {
			return err
		}
	}
}

// kindsPresent: every vault and Hub in the report carries its kind — the
// field this app reads the icon from.
func kindsPresent(st Status) bool {
	for _, vt := range st.Vaults {
		if vt.Kind == "" {
			return false
		}
	}
	for _, h := range st.Hubs {
		if h.Kind == "" {
			return false
		}
	}
	return true
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
	held, err := c.Lock(c.Layout)
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
	if err == nil && !kindsPresent(st) {
		// An agent that names no kind for a state is older than this
		// app's reading of the socket: nothing is summarized from words.
		err = ErrOldAgent
	}
	c.status, c.known, c.lastErr = st, err == nil, err
	c.logPath = c.logFile()
	c.canStart = c.Start != nil && c.StartProgram != nil && c.StartProgram() != ""
	// What answers speaks for itself, however badly.
	if c.startNotice != "" && c.notice == c.startNotice && !nobodyThere(err) {
		c.notice, c.startNotice = "", ""
	}
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
		Home:     c.Home,
		LogPath:  c.logPath,
		Pairing:  c.pairing,
		CanAsk:   c.Asker != nil,
		CanStart: c.canStart,
		Starting: c.starting || c.settling,
		Notice:   notice,
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
func (c *Controller) openVault(ctx context.Context, path string, done chan<- openOutcome) {
	if path == "" {
		return
	}
	for _, vt := range c.view.Vaults {
		if vt.Path == path {
			c.openPath(ctx, path, done)
			return
		}
	}
}

func (c *Controller) openLog(ctx context.Context, done chan<- openOutcome) {
	if c.logPath != "" {
		c.openPath(ctx, c.logPath, done)
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
// The outcome comes back on done — never dropped while the loop runs (ctx).
func (c *Controller) openPath(ctx context.Context, path string, done chan<- openOutcome) {
	if c.Open == nil {
		return
	}
	go func() {
		out := openOutcome{path: path}
		if err := c.Open(context.Background(), path); err != nil {
			out.notice = "Could not open it: " + err.Error()
		}
		select {
		case done <- out:
		case <-ctx.Done():
		}
	}()
}

// openOutcome is how an open of path ended: notice says why it failed, ""
// when it opened.
type openOutcome struct {
	path   string
	notice string
}

// startOutcome is how a start from the menu ended: the notice it leaves.
type startOutcome struct {
	notice string
	failed bool
}

// startService runs vaultsync start off the loop: it takes seconds, and up
// to half a minute when an old job is on its way out. It decides nothing
// itself — whether the service may start and what that means is the
// agent's, and the menu shows what it said.
func (c *Controller) startService(ctx context.Context, done chan<- startOutcome) error {
	if !c.view.Start.Enabled {
		return nil
	}
	// The agent is asked first: a start would restart one that has come
	// up since the last poll. What ends a poll's refresh ends this one.
	if err := c.refresh(ctx); err != nil {
		return err
	}
	if !c.view.Start.Enabled {
		return nil
	}
	program := c.StartProgram()
	if program == "" {
		return nil
	}
	c.starting = true
	c.notice = ""
	c.render()
	go func() {
		// Not the menu's context: a quit must not end the start between
		// taking the old job down and loading it again.
		said, err := c.Start(context.Background(), program)
		if err != nil {
			done <- startOutcome{notice: "Could not start VaultSync: " + err.Error(), failed: true}
			return
		}
		done <- startOutcome{notice: said}
	}()
	return nil
}

// startWaits is how long to wait before each time the agent is asked again
// after a start.
func (c *Controller) startWaits() []time.Duration {
	if len(c.StartWaits) > 0 {
		return c.StartWaits
	}
	return []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
}

// noStartAnswer is the notice for a service that started and has no agent
// answering when the wait ends: one whose run ends before its socket counts
// as running for a moment each time it is restarted, so "running again"
// would stand there.
const noStartAnswer = "VaultSync started but does not answer yet — vaultsync status in a terminal tells where it stands"

// nobodyThere: the last look found no agent to answer.
func nobodyThere(err error) bool {
	return errors.Is(err, ErrNoAgent) || errors.Is(err, ErrNoSocket)
}

// endOfWait decides what a start from the menu came to, once, by how things
// stand when its asks end: a first answer proves little, since a run can end
// right after it. Whatever answers has spoken for itself. With nobody there
// the menu says so, keeping what a pairing or an open has said since; a later
// poll still takes the agent's report when one comes.
func (c *Controller) endOfWait(ctx context.Context) {
	c.settling = false
	if nobodyThere(c.lastErr) {
		if c.notice == "" || c.notice == c.startNotice {
			c.notice, c.startNotice = noStartAnswer, noStartAnswer
		}
		c.notify(ctx, noStartAnswer)
	}
	c.render()
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
func (c *Controller) startPairing(ctx context.Context, done chan<- string) error {
	if c.pairing || c.Asker == nil || !c.view.Pair.Enabled {
		return nil
	}
	// The agent is asked for its status first: one gone since the last
	// poll is said now, before the person types a code for nobody. What
	// ends a poll's refresh — a second instance, a quit — ends this one.
	if err := c.refresh(ctx); err != nil {
		return err
	}
	// Known means the refresh reached the agent through c.cl.
	cl := c.cl
	if !c.known || !c.view.Pair.Enabled || cl == nil {
		return nil
	}
	c.pairing = true
	c.notice = ""
	c.render()
	go func() {
		line, err := Pair(ctx, cl, c.Asker)
		if err != nil && !errors.Is(err, context.Canceled) {
			// The fact for the log; the words — which may name a vault or
			// a folder — go to the menu's notice and the dialog. A quit is
			// no error.
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
	return nil
}

// errNoAnswer: the agent took longer than the request's limit — busy, or
// stuck; the next refresh asks again.
var errNoAnswer = errors.New("VaultSync did not answer in time; it may be busy, and is asked again")

// pairingTimeoutNotice says what a pairing request that ran out of time
// leaves behind: the agent may still finish what it had taken on.
const pairingTimeoutNotice = "VaultSync did not answer a pairing request within 15 minutes; vaultsync status shows what came of it, and Pair a Vault… starts over"

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
