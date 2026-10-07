package main

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/psimaker/vaultsync/hub/syncthing"
)

// status says what syncs where — also when the engine is stopped, from the
// engine's own config file. "Up to date" is said only while the Hub is
// connected.

type folderSummary struct {
	State       string `json:"state"`
	Error       string `json:"error"`
	Errors      int    `json:"errors"`
	GlobalBytes int64  `json:"globalBytes"`
	InSyncBytes int64  `json:"inSyncBytes"`
	NeedBytes   int64  `json:"needBytes"`
	NeedTotal   int    `json:"needTotalItems"`
	WatchError  string `json:"watchError"`
}

// remoteCompletion is how far a Hub has this folder (/rest/db/completion).
type remoteCompletion struct {
	Completion  float64 `json:"completion"`
	NeedItems   int     `json:"needItems"`
	NeedDeletes int     `json:"needDeletes"`
	RemoteState string  `json:"remoteState"` // unknown, notSharing, paused, valid
}

type systemStatusView struct {
	MyID           string `json:"myID"`
	LastDialStatus map[string]struct {
		Error *string `json:"error"`
	} `json:"lastDialStatus"`
}

type connectionsView struct {
	Connections map[string]struct {
		Connected bool   `json:"connected"`
		Type      string `json:"type"`
	} `json:"connections"`
}

func (a *app) status(ctx context.Context) error {
	w := a.out
	fmt.Fprintf(w, "VaultSync %s — sync engine Syncthing %s\n", version, strings.TrimPrefix(syncthingVersion, "v"))
	eng := a.engine()
	if !eng.prepared() {
		fmt.Fprintln(w, "VaultSync is not set up on this computer yet. Run vaultsync setup.")
		return nil
	}
	background := a.backgroundEngine()
	switch {
	case !a.svc.installed():
		fmt.Fprintln(w, "Background service: not installed (vaultsync setup installs it; vaultsync run runs the engine in this terminal)")
	case background:
		fmt.Fprintln(w, "Background service: running")
	default:
		fmt.Fprintln(w, "Background service: stopped — vaultsync start resumes it")
	}
	st, err := loadState(a.lay.State)
	if err != nil {
		return err
	}
	// The running agent answers first (control.go): one truth for the
	// terminal and the menu-bar app. Without one — an older agent, no agent
	// — the engine is asked directly.
	actx, cancel := context.WithTimeout(ctx, 10*time.Second)
	cs, aerr := a.controlClient(st).status(actx)
	cancel()
	switch {
	case aerr == nil && cs.Engine != "running":
		fmt.Fprintln(w, "Sync engine: starting — VaultSync is bringing it up")
		_, err := io.WriteString(w, cs.Text)
		return err
	case aerr == nil:
		fmt.Fprintln(w, "Sync engine: running")
		_, err := io.WriteString(w, cs.Text)
		return err
	case !errors.Is(aerr, errNoAgent):
		// An agent answered and failed: that is the news, not whatever a
		// look at the engine would say.
		return fmt.Errorf("the running VaultSync agent could not report: %w", aerr)
	}
	client, err := eng.client(st)
	if err != nil {
		return err
	}
	pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	up := client.Ping(pctx) == nil
	cancel()
	if !up {
		fmt.Fprintln(w, "Sync engine: not running — nothing syncs right now")
		_, err := io.WriteString(w, a.offlineReport().Text)
		return err
	}
	fmt.Fprintln(w, "Sync engine: running")
	return a.onlineStatus(ctx, w, client, background)
}

// backgroundEngine says whether the engine belongs to the background service.
// An installed service that is stopped (vaultsync stop, or a setup that fell
// back to --no-service) leaves the engine to a `vaultsync run` in a terminal,
// whose remedies differ: the terminal app needs the access, and that run
// the restart.
func (a *app) backgroundEngine() bool {
	return a.svc.installed() && a.svc.running()
}

// statusReport is what is known about syncing right now: the lines
// vaultsync status prints below the engine line, and the facts behind them
// for the menu-bar app (#176).
type statusReport struct {
	// Paused: syncing is paused on this computer — every Hub this engine
	// knows is paused in it (vaultsync pause).
	Paused   bool            `json:"paused"`
	Hubs     []hubStatus     `json:"hubs"`
	Vaults   []vaultStatus   `json:"vaults"`
	Waiting  []waitingStatus `json:"waiting"`
	Attempts []attemptStatus `json:"attempts"`
	// Text is the report as vaultsync status prints it.
	Text string `json:"text"`
}

type hubStatus struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
}

type vaultStatus struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Path  string `json:"path"`
	State string `json:"state"`
}

type waitingStatus struct {
	Label string `json:"label"`
	Hub   string `json:"hub"`
}

type attemptStatus struct {
	Vault string `json:"vault"`
	What  string `json:"what"`
	At    string `json:"at"`
}

func (a *app) onlineStatus(ctx context.Context, w io.Writer, c *syncthing.Client, background bool) error {
	r, err := a.report(ctx, c, background)
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, r.Text)
	return err
}

// report gathers the status from the engine and renders it.
func (a *app) report(ctx context.Context, c *syncthing.Client, background bool) (statusReport, error) {
	r := emptyReport()
	var sys systemStatusView
	if err := c.Get(ctx, "/rest/system/status", &sys); err != nil {
		return r, err
	}
	devices, err := c.Devices(ctx)
	if err != nil {
		return r, err
	}
	var conns connectionsView
	_ = c.Get(ctx, "/rest/system/connections", &conns)
	var text strings.Builder
	w := &text
	anyConnected := false
	pausedHubs := 0
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Hub")
	for _, d := range devices {
		if d.DeviceID == sys.MyID {
			continue
		}
		name := d.Name
		if name == "" {
			name = "(unnamed Hub)"
		}
		state := "not connected"
		switch cn, ok := conns.Connections[d.DeviceID]; {
		case d.Paused:
			pausedHubs++
			state = "paused on this computer — vaultsync resume starts syncing again"
		case ok && cn.Connected:
			anyConnected = true
			state = "connected"
			if strings.HasPrefix(cn.Type, "relay") {
				state = "connected through a relay (slower; a direct connection is not possible right now)"
			}
		}
		r.Hubs = append(r.Hubs, hubStatus{ID: d.DeviceID, Name: name, State: state})
		fmt.Fprintf(w, "  %-22s %s\n", name, state)
	}
	if len(r.Hubs) == 0 {
		fmt.Fprintln(w, "  none yet — run vaultsync pair")
	}
	r.Paused = len(r.Hubs) > 0 && pausedHubs == len(r.Hubs)
	if len(r.Hubs) > 0 && !anyConnected && !r.Paused && a.goos == "darwin" && localNetworkRefused(sys) {
		fmt.Fprintln(w, "  "+localNetworkHint(background))
	}

	folders, err := c.Folders(ctx)
	if err != nil {
		return r, err
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Vaults")
	if len(folders) == 0 {
		fmt.Fprintln(w, "  none yet — run vaultsync pair")
	}
	sort.Slice(folders, func(i, j int) bool { return strings.ToLower(folders[i].Label) < strings.ToLower(folders[j].Label) })
	for _, f := range folders {
		state := "unknown"
		var sum folderSummary
		if err := c.Get(ctx, "/rest/db/status?folder="+url.QueryEscape(f.ID), &sum); err == nil {
			remote := map[string]remoteCompletion{}
			for _, d := range f.Devices {
				if d.DeviceID == sys.MyID {
					continue
				}
				var rc remoteCompletion
				if err := c.Get(ctx, "/rest/db/completion?folder="+url.QueryEscape(f.ID)+"&device="+url.QueryEscape(d.DeviceID), &rc); err == nil {
					remote[d.DeviceID] = rc
				}
			}
			state = describeFolder(f, sum, conns, remote, sys.MyID, background)
		}
		// Paused here: the connections are, so a state that only says what
		// the connection would say reads "paused on this computer" — while
		// everything else stays as it is: an error with its remedy, the
		// folder's own pause, files that could not sync, work the engine is
		// still doing on what it had already taken in.
		if r.Paused && connectionState(state) {
			state = "paused on this computer"
		}
		r.Vaults = append(r.Vaults, vaultStatus{ID: f.ID, Label: f.Label, Path: f.Path, State: state})
		fmt.Fprintf(w, "  %-14s %-28s %s\n", f.Label, tildePath(a.home, f.Path), state)
	}
	if waiting, err := pendingFromHubs(ctx, c, sys.MyID); err == nil && len(waiting) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Waiting to be set up")
		for _, p := range waiting {
			r.Waiting = append(r.Waiting, waitingStatus{Label: p.label, Hub: p.hub})
			fmt.Fprintf(w, "  %-14s shared by %s — run vaultsync pair to finish setup\n", p.label, p.hub)
		}
	}
	configured := map[string]bool{}
	for _, f := range folders {
		configured[f.ID] = true
	}
	r.Attempts = append(r.Attempts, a.printAttempts(w, configured)...)
	r.Text = text.String()
	return r, nil
}

// connectionState: a folder state that says nothing but what the
// connection to the Hub is doing — the ones a pause of that connection
// replaces. Anything else (an error, the folder's own pause, files that
// could not sync, scanning, syncing or items still to sync, no answer)
// stays visible while paused.
func connectionState(state string) bool {
	return state == "up to date" || state == "paused on your Hub" || strings.HasPrefix(state, "waiting for your Hub") || strings.HasPrefix(state, "uploading to your Hub")
}

// emptyReport is a report with every list present (never null in JSON).
func emptyReport() statusReport {
	return statusReport{Hubs: []hubStatus{}, Vaults: []vaultStatus{}, Waiting: []waitingStatus{}, Attempts: []attemptStatus{}}
}

// describeFolder says where a vault stands. "Up to date" needs both sides:
// nothing left to pull here, and every connected Hub reporting the folder
// active and complete — a Hub that is still receiving this computer's files
// is "uploading".
// background says whether the engine runs as the background service (the
// remedies differ from a `vaultsync run` in a terminal).
func describeFolder(f syncthing.FolderConfig, s folderSummary, conns connectionsView, remote map[string]remoteCompletion, myID string, background bool) string {
	if f.Paused {
		return "paused"
	}
	if s.State == "error" || s.Error != "" {
		msg := s.Error
		if strings.Contains(msg, "operation not permitted") || strings.Contains(msg, "permission denied") {
			// The engine looks at a folder it could not open again only at its
			// next full scan, an hour later by default: a restart applies the
			// new permission now. Files & Folders covers Documents, Desktop,
			// Downloads and other volumes; Full Disk Access would be more than
			// a vault needs.
			if background {
				msg += " — allow access to this folder (on a Mac: allow “vaultsync” in System Settings → Privacy & Security → Files & Folders), then run vaultsync stop and vaultsync start"
			} else {
				msg += " — allow access to this folder (on a Mac: allow the app you run vaultsync in, Terminal for example, in System Settings → Privacy & Security → Files & Folders), then restart vaultsync run"
			}
		}
		return "error: " + msg
	}
	switch s.State {
	case "scanning", "scan-waiting":
		return "scanning the folder"
	case "syncing", "sync-preparing", "sync-waiting":
		if s.GlobalBytes > 0 {
			return fmt.Sprintf("syncing — %d %%", s.InSyncBytes*100/s.GlobalBytes)
		}
		return "syncing"
	}
	if s.Errors > 0 {
		return fmt.Sprintf("%d files could not sync — see the engine's log", s.Errors)
	}
	connected := 0
	lowest := 100.0
	outstanding := 0
	for _, d := range f.Devices {
		if d.DeviceID == myID {
			continue
		}
		if cn, ok := conns.Connections[d.DeviceID]; !ok || !cn.Connected {
			continue
		}
		connected++
		rc, ok := remote[d.DeviceID]
		switch {
		case !ok || rc.RemoteState == "unknown" || rc.RemoteState == "notSharing":
			return "waiting for your Hub to take it"
		case rc.RemoteState == "paused":
			return "paused on your Hub"
		case rc.RemoteState != "valid":
			return "waiting for your Hub to take it"
		}
		if rc.Completion < lowest {
			lowest = rc.Completion
		}
		// The percentage counts bytes: an empty note or a deletion still
		// outstanding leaves it at 100.
		outstanding += rc.NeedItems + rc.NeedDeletes
	}
	// What is still to pull is said before the connection's state: it is
	// this computer's work, and it stays in sight while the connection is
	// paused.
	if s.NeedTotal > 0 {
		return fmt.Sprintf("%d items left to sync", s.NeedTotal)
	}
	if connected == 0 {
		return "waiting for your Hub"
	}
	if lowest < 100 {
		return fmt.Sprintf("uploading to your Hub — %d %%", int(lowest))
	}
	if outstanding == 1 {
		return "uploading to your Hub — 1 item left"
	}
	if outstanding > 1 {
		return fmt.Sprintf("uploading to your Hub — %d items left", outstanding)
	}
	return "up to date"
}

// localNetworkSetting is where macOS lists the programs that may use the
// local network; it lists the agent by its file name, “vaultsync”.
const localNetworkSetting = "System Settings → Privacy & Security → Local Network"

// localNetworkHint is status's remedy when macOS refuses the engine's
// connections on the local network.
func localNetworkHint(background bool) string {
	restart := "then run vaultsync stop and vaultsync start."
	if !background {
		restart = "then restart vaultsync run."
	}
	return "macOS refused the connection on your local network. Allow “vaultsync” in " + localNetworkSetting + ", " + restart
}

// localNetworkRefused: the engine's last dials to local addresses failed
// with "no route to host" — how macOS Local Network privacy shows to a
// background process that was not allowed.
func localNetworkRefused(sys systemStatusView) bool {
	for addr, st := range sys.LastDialStatus {
		if st.Error == nil || !strings.Contains(*st.Error, "no route to host") {
			continue
		}
		u, err := url.Parse(addr)
		if err != nil {
			continue
		}
		host, _, err := net.SplitHostPort(u.Host)
		if err != nil {
			continue
		}
		if ip := net.ParseIP(host); ip != nil && (ip.IsPrivate() || ip.IsLinkLocalUnicast()) {
			return true
		}
	}
	return false
}

// offlineReport is the status while the engine does not answer: the vaults
// from the engine's config file, and the pairings that did not finish —
// the same for the terminal and the control socket.
func (a *app) offlineReport() statusReport {
	r := emptyReport()
	var text strings.Builder
	w := &text
	configured := map[string]bool{}
	if f, err := os.Open(a.engine().configPath()); err == nil {
		defer f.Close()
		var cfg struct {
			Folders []struct {
				ID    string `xml:"id,attr"`
				Label string `xml:"label,attr"`
				Path  string `xml:"path,attr"`
			} `xml:"folder"`
		}
		if xml.NewDecoder(io.LimitReader(f, 16<<20)).Decode(&cfg) == nil {
			fmt.Fprintln(w)
			fmt.Fprintln(w, "Vaults (not syncing while the engine is stopped)")
			if len(cfg.Folders) == 0 {
				fmt.Fprintln(w, "  none yet")
			}
			for _, fo := range cfg.Folders {
				configured[fo.ID] = true
				r.Vaults = append(r.Vaults, vaultStatus{ID: fo.ID, Label: fo.Label, Path: fo.Path, State: "not syncing — the engine is stopped"})
				fmt.Fprintf(w, "  %-14s %s\n", fo.Label, tildePath(a.home, fo.Path))
			}
		}
	}
	r.Attempts = append(r.Attempts, a.printAttempts(w, configured)...)
	r.Text = text.String()
	return r
}

// printAttempts shows pairings that did not finish, unless the vault was set
// up after all (configured, or a later attempt was accepted), and returns
// them.
func (a *app) printAttempts(w io.Writer, configured map[string]bool) []attemptStatus {
	all, err := loadAttempts(a.lay)
	if err != nil {
		fmt.Fprintln(w)
		fmt.Fprintln(w, err)
		return nil
	}
	done := map[string]bool{}
	for _, at := range all {
		if at.State == "accepted" && at.VaultID != "" {
			done[at.VaultID] = true
		}
	}
	var open []pairAttempt
	for _, at := range all {
		if at.State != "unknown" && at.State != "shared" && at.State != "sending" {
			continue
		}
		if at.VaultID != "" && (done[at.VaultID] || configured[at.VaultID]) {
			continue
		}
		open = append(open, at)
	}
	if len(open) == 0 {
		return nil
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Unfinished pairings")
	var out []attemptStatus
	for _, at := range open {
		what := "your Hub is sharing it; run vaultsync pair to finish setup"
		if at.State != "shared" {
			what = "VaultSync couldn’t confirm whether your Hub shared it; check on the Hub with vaultsync-hub status before pairing again"
		}
		when := at.At.Local().Format("Jan 2 15:04")
		out = append(out, attemptStatus{Vault: at.Vault, What: what, At: when})
		fmt.Fprintf(w, "  %-14s %s (%s)\n", at.Vault, what, when)
	}
	return out
}
