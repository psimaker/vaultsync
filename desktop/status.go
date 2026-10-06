package main

import (
	"context"
	"encoding/xml"
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
	switch {
	case !a.svc.installed():
		fmt.Fprintln(w, "Background service: not installed (vaultsync setup installs it; vaultsync run runs the engine in this terminal)")
	case a.svc.running():
		fmt.Fprintln(w, "Background service: running")
	default:
		fmt.Fprintln(w, "Background service: stopped — vaultsync start resumes it")
	}
	st, err := loadState(a.lay.State)
	if err != nil {
		return err
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
		a.printAttempts(w, a.offlineFolders(w, eng))
		return nil
	}
	fmt.Fprintln(w, "Sync engine: running")
	return a.onlineStatus(ctx, w, client)
}

func (a *app) onlineStatus(ctx context.Context, w io.Writer, c *syncthing.Client) error {
	var sys systemStatusView
	if err := c.Get(ctx, "/rest/system/status", &sys); err != nil {
		return err
	}
	devices, err := c.Devices(ctx)
	if err != nil {
		return err
	}
	var conns connectionsView
	_ = c.Get(ctx, "/rest/system/connections", &conns)
	hubNames := map[string]string{}
	anyConnected := false
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Hub")
	hubs := 0
	for _, d := range devices {
		if d.DeviceID == sys.MyID {
			continue
		}
		hubs++
		name := d.Name
		if name == "" {
			name = "(unnamed Hub)"
		}
		hubNames[d.DeviceID] = name
		state := "not connected"
		if cn, ok := conns.Connections[d.DeviceID]; ok && cn.Connected {
			anyConnected = true
			state = "connected"
			if strings.HasPrefix(cn.Type, "relay") {
				state = "connected through a relay (slower; a direct connection is not possible right now)"
			}
		}
		fmt.Fprintf(w, "  %-22s %s\n", name, state)
	}
	if hubs == 0 {
		fmt.Fprintln(w, "  none yet — run vaultsync pair")
	}
	if hubs > 0 && !anyConnected && a.goos == "darwin" && localNetworkRefused(sys) {
		fmt.Fprintln(w, "  macOS refused the connection on your local network. Allow VaultSync in System Settings → Privacy & Security → Local Network, then run vaultsync stop and vaultsync start.")
	}

	folders, err := c.Folders(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Vaults")
	if len(folders) == 0 {
		fmt.Fprintln(w, "  none yet — run vaultsync pair")
	}
	sort.Slice(folders, func(i, j int) bool { return strings.ToLower(folders[i].Label) < strings.ToLower(folders[j].Label) })
	for _, f := range folders {
		var sum folderSummary
		state := "unknown"
		if err := c.Get(ctx, "/rest/db/status?folder="+url.QueryEscape(f.ID), &sum); err == nil {
			state = describeFolder(f, sum, conns, sys.MyID)
		}
		fmt.Fprintf(w, "  %-14s %-28s %s\n", f.Label, tildePath(a.home, f.Path), state)
	}
	if waiting, err := pendingFromHubs(ctx, c, sys.MyID); err == nil && len(waiting) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Waiting to be set up")
		for _, p := range waiting {
			fmt.Fprintf(w, "  %-14s shared by %s — run vaultsync pair to finish setup\n", p.label, p.hub)
		}
	}
	configured := map[string]bool{}
	for _, f := range folders {
		configured[f.ID] = true
	}
	a.printAttempts(w, configured)
	return nil
}

func describeFolder(f syncthing.FolderConfig, s folderSummary, conns connectionsView, myID string) string {
	if f.Paused {
		return "paused"
	}
	if s.State == "error" || s.Error != "" {
		msg := s.Error
		if strings.Contains(msg, "operation not permitted") || strings.Contains(msg, "permission denied") {
			msg += " — allow access to this folder (on a Mac: System Settings → Privacy & Security → Files and Folders or Full Disk Access)"
		}
		return "error: " + msg
	}
	connected := false
	for _, d := range f.Devices {
		if d.DeviceID == myID {
			continue
		}
		if cn, ok := conns.Connections[d.DeviceID]; ok && cn.Connected {
			connected = true
		}
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
	if !connected {
		return "waiting for your Hub"
	}
	if s.NeedTotal > 0 {
		return fmt.Sprintf("%d items left to sync", s.NeedTotal)
	}
	return "up to date"
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

// offlineFolders lists the vaults from the engine's config file and returns
// their IDs.
func (a *app) offlineFolders(w io.Writer, eng engine) map[string]bool {
	ids := map[string]bool{}
	f, err := os.Open(eng.configPath())
	if err != nil {
		return ids
	}
	defer f.Close()
	var cfg struct {
		Folders []struct {
			ID    string `xml:"id,attr"`
			Label string `xml:"label,attr"`
			Path  string `xml:"path,attr"`
		} `xml:"folder"`
	}
	if xml.NewDecoder(io.LimitReader(f, 16<<20)).Decode(&cfg) != nil {
		return ids
	}
	for _, fo := range cfg.Folders {
		ids[fo.ID] = true
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Vaults (not syncing while the engine is stopped)")
	if len(cfg.Folders) == 0 {
		fmt.Fprintln(w, "  none yet")
	}
	for _, fo := range cfg.Folders {
		fmt.Fprintf(w, "  %-14s %s\n", fo.Label, tildePath(a.home, fo.Path))
	}
	return ids
}

// printAttempts shows pairings that did not finish, unless the vault was set
// up after all (configured, or a later attempt was accepted).
func (a *app) printAttempts(w io.Writer, configured map[string]bool) {
	all := loadAttempts(a.lay)
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
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Unfinished pairings")
	for _, at := range open {
		what := "your Hub is sharing it; run vaultsync pair to finish setup"
		if at.State != "shared" {
			what = "VaultSync couldn’t confirm whether your Hub shared it; check on the Hub with vaultsync-hub status before pairing again"
		}
		fmt.Fprintf(w, "  %-14s %s (%s)\n", at.Vault, what, at.At.Local().Format("Jan 2 15:04"))
	}
}
