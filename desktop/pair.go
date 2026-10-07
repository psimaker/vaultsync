package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/psimaker/vaultsync/hub/join"
	"github.com/psimaker/vaultsync/hub/pairing"
	"github.com/psimaker/vaultsync/hub/syncthing"
)

// Pairing this computer with a Hub and setting up one vault. The doctrine is
// the app's (AGENTS.md rules 1, 2, 3, 7, 8): the code goes to the one Hub the
// person chose (decision 045); a folder that holds files syncs only with
// their explicit consent and only with a vault this pairing created; nothing
// is ever merged, moved, re-pointed or deleted; every check runs again right
// before the folder is added, after every wait.

const (
	hubDiscoveryWait    = 3 * time.Second
	hubHandshakeTimeout = 25 * time.Second
	hubProvisionTimeout = 40 * time.Second
	// hubSessionDeadline: the Hub forgets a session five minutes after the
	// code was entered; past four, this side reconnects first.
	hubSessionDeadline = 4 * time.Minute
	defaultHubSyncPort = 22000
	maxHubNameRunes    = 64
	attemptsKept       = 10
)

var (
	errCancelled      = errors.New("cancelled")
	errOutcomeUnknown = errors.New("the outcome is unknown")
	errPairingBusy    = errors.New("another vaultsync pair is running on this computer — wait for it to finish")
)

// refusal is a decision the person reads; it carries its own wording.
type refusal struct{ msg string }

func (r *refusal) Error() string { return r.msg }

func refuse(format string, a ...any) error { return &refusal{fmt.Sprintf(format, a...)} }

type pairOptions struct {
	code, hub, vault, path, name string
	create, yes                  bool
}

// pairEnv is everything the flow touches outside its own logic; tests and the
// E2E replace parts of it.
type pairEnv struct {
	goos, home string
	getenv     func(string) string
	lay        layout
	engine     *syncthing.Client
	discover   func(context.Context) ([]pairing.DiscoveredHub, error)
	dial       func(addr string) *pairing.Client
	registries []string
	scanRoots  []string
	cloud      func() []cloudRoot
	userST     func() (userSyncthing, bool)
	unitDir    string
	// pendingTimeout bounds the wait for the Hub's share to arrive.
	pendingTimeout time.Duration
	// hubSyncAddress replaces the address hint for the Hub's sync port
	// (tcp://<Hub IP>:22000); the loopback E2E sets it.
	hubSyncAddress string
	deviceName     string
	now            func() time.Time
}

type pairSession struct {
	env  pairEnv
	t    *term
	opts pairOptions

	myID        string
	engineStart string
	hub         pairing.DiscoveredHub
	client      *pairing.Client
	code        string
	hello       pairing.HubPayload
	pairedAt    time.Time
	seq         uint64
}

type planKind int

const (
	planDownload planKind = iota // an existing Hub vault into a new or empty folder
	planNew                      // a new Hub vault started from a folder on this computer
)

type plan struct {
	kind     planKind
	vault    pairing.VaultInfo // planDownload: the catalogue entry
	name     string            // planNew: the name on the Hub
	path     string            // resolved local folder
	hadFiles bool              // what the folder held when the person decided
	// The folder the person decided on, by identity: dir when it existed,
	// otherwise its nearest existing ancestor (anchor). A replaced folder, a
	// vanished one or a disconnected disk is noticed before anything syncs.
	dir        os.FileInfo
	anchorPath string
	anchor     os.FileInfo
}

// withIdentity records which folder the plan means.
func (p plan) withIdentity() plan {
	if fi, err := os.Stat(p.path); err == nil {
		p.dir = fi
		return p
	}
	for a := filepath.Dir(p.path); ; a = filepath.Dir(a) {
		if fi, err := os.Stat(a); err == nil {
			p.anchorPath, p.anchor = a, fi
			return p
		}
		if filepath.Dir(a) == a {
			return p
		}
	}
}

func (p plan) label() string {
	if p.kind == planDownload {
		return p.vault.Label
	}
	return p.name
}

func (s *pairSession) run(ctx context.Context) error {
	unlock, err := lockFile(filepath.Join(s.env.lay.Base, "pair.lock"))
	if errors.Is(err, ErrEngineRunning) {
		return errPairingBusy
	}
	if err != nil {
		return err
	}
	defer unlock()
	if s.myID, err = s.env.engine.MyID(ctx); err != nil {
		return fmt.Errorf("the sync engine does not answer: %w", err)
	}
	if s.engineStart, err = engineStartTime(ctx, s.env.engine); err != nil {
		return fmt.Errorf("the sync engine does not answer: %w", err)
	}
	if done, err := s.resumePending(ctx); done || err != nil {
		return err
	}
	if err := s.findHub(ctx); err != nil {
		return err
	}
	if err := s.connect(ctx); err != nil {
		return err
	}
	if s.hello.Vaults == nil {
		// The Hub sends no list (null, not an empty one) when it cannot read
		// its own vaults; nothing may be decided on that.
		return refuse("Your Hub could not list its vaults just now. Run vaultsync pair again in a moment.")
	}
	if err := s.addHubDevice(ctx); err != nil {
		return err
	}
	p, err := s.choose(ctx)
	var already *alreadySyncing
	if errors.As(err, &already) {
		// A repeated setup with the same flags: nothing to do, nothing wrong.
		s.t.say("✓ %s already syncs at %s.", quoted(already.label), already.path)
		return nil
	}
	if err != nil {
		return err
	}
	return s.finish(ctx, p)
}

// alreadySyncing: the chosen Hub vault already syncs at the chosen folder on
// this computer (#228). Not a refusal — the setup ends as succeeded.
type alreadySyncing struct{ label, path string }

func (e *alreadySyncing) Error() string {
	return fmt.Sprintf("%s already syncs at %s.", quoted(e.label), e.path)
}

// --- the Hub ----------------------------------------------------------------

func (s *pairSession) findHub(ctx context.Context) error {
	if s.opts.hub != "" {
		addr, err := pairing.LocalHubAddress(s.opts.hub)
		if errors.Is(err, pairing.ErrNotLocal) {
			return refuse("%s is not on your local network. VaultSync pairs only with a Hub on your own network.", s.opts.hub)
		}
		if err != nil {
			return refuse("%s is not a Hub address. Name your Hub by its IP address, for example: vaultsync pair --hub 192.168.1.20", s.opts.hub)
		}
		s.hub = pairing.DiscoveredHub{Address: addr}
		return nil
	}
	fmt.Fprint(s.t.out, "  Looking for your Hub on this network… ")
	found, err := s.env.discover(ctx)
	if err != nil {
		s.t.blank()
		return refuse("Could not search this network for your Hub (%v). If this computer is on the same network as your Hub, name it instead: vaultsync pair --hub 192.168.1.20%s", err, macLocalNetworkNote(s.env.goos))
	}
	// Answers are hints from anyone on the network: only local addresses.
	var hubs []pairing.DiscoveredHub
	for _, h := range found {
		if addr, err := pairing.LocalHubAddress(h.Address); err == nil {
			h.Address = addr
			hubs = append(hubs, h)
		}
	}
	switch len(hubs) {
	case 0:
		s.t.say("none answered.")
		return refuse("No Hub answered on this network. Pairing needs this computer and your Hub on the same network (afterwards they sync from anywhere). If they are, name your Hub: vaultsync pair --hub 192.168.1.20%s", macLocalNetworkNote(s.env.goos))
	case 1:
		s.t.say("found %s (%s)", quoted(hubName(hubs[0])), hostOf(hubs[0].Address))
	default:
		s.t.say("%d Hubs answered:", len(hubs))
	}
	var choose func([]pairing.DiscoveredHub) (int, error)
	if s.t.interactive() {
		choose = s.chooseHub
	}
	hub, err := join.PickHub(hubs, choose)
	var several *join.SeveralHubsError
	if errors.As(err, &several) {
		return refuse("%s. VaultSync sends a code only to the Hub you choose.", several.Error())
	}
	if err != nil {
		return err
	}
	s.hub = hub
	return nil
}

// chooseHub lists the Hubs that answered; there is no default — an empty
// answer never sends the code to the first responder.
func (s *pairSession) chooseHub(hubs []pairing.DiscoveredHub) (int, error) {
	for i, h := range hubs {
		s.t.say("    %d) %-20s %s", i+1, hubName(h), hostOf(h.Address))
	}
	for {
		a, err := s.t.ask("  Which Hub printed your code? Enter a number: ")
		if err != nil {
			return 0, err
		}
		if n, err := strconv.Atoi(a); err == nil && n >= 1 && n <= len(hubs) {
			return n - 1, nil
		}
	}
}

func hubName(h pairing.DiscoveredHub) string {
	if h.Name != "" {
		return h.Name
	}
	return "your Hub"
}

func hostOf(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// connect asks for the code (unless given) and runs the handshake. A wrong
// code can be typed again: each try counts on the Hub, and five lock it.
func (s *pairSession) connect(ctx context.Context) error {
	code := s.opts.code
	for {
		if code == "" {
			a, err := s.t.ask("  The code your Hub printed (like TULIP-ANCHOR-42): ")
			if errors.Is(err, errNoTerminal) {
				return refuse("Pass the code your Hub printed: --code WORD-WORD-NN — there is no terminal to ask on.")
			}
			if err != nil {
				return err
			}
			code = a
		}
		canonical, err := pairing.NormalizeCode(code)
		if err != nil {
			if !s.t.interactive() {
				return refuse("That doesn’t look like a code from your Hub. Check the two words and the number.")
			}
			s.t.say("  That doesn’t look like a code from your Hub. Check the two words and the number.")
			code = ""
			continue
		}
		err = s.handshake(ctx, canonical)
		if err == nil {
			s.code = canonical
			s.t.say("✓ Connected to %s.", quoted(s.hubLabel()))
			return nil
		}
		if pairing.FailureKind(err) == pairing.KindCodeRejected && s.t.interactive() {
			s.t.say("  %s", handshakeText(err, s))
			code = ""
			continue
		}
		return refuse("%s", handshakeText(err, s))
	}
}

func (s *pairSession) handshake(ctx context.Context, code string) error {
	hctx, cancel := context.WithTimeout(ctx, hubHandshakeTimeout)
	defer cancel()
	client := s.env.dial(s.hub.Address)
	hello, err := client.Handshake(hctx, code)
	if err != nil {
		return err
	}
	if hello.HubDeviceID == s.myID {
		return errors.New("the Hub announced this computer's own device ID")
	}
	s.client, s.hello, s.pairedAt, s.seq = client, hello, s.env.now(), 0
	if s.hub.Name == "" {
		s.hub.Name = pairing.SanitizeName(hello.HubName)
	}
	return nil
}

func (s *pairSession) hubLabel() string {
	if n := pairing.SanitizeName(s.hello.HubName); n != "" {
		return n
	}
	return hubName(s.hub)
}

// handshakeText is the iPhone's wording for each way a pairing can fail.
func handshakeText(err error, s *pairSession) string {
	switch pairing.FailureKind(err) {
	case pairing.KindCodeRejected:
		return "Your Hub didn’t accept this code. Check the two words and the number — too many wrong tries lock the code."
	case pairing.KindAuthFailed:
		return "This Hub could not prove that it knows the code. Make sure you’re pairing with your own Hub, then try again."
	case pairing.KindCodeExpired:
		return "This code has expired. Create a new one on your Hub with “vaultsync-hub code”."
	case pairing.KindCodeLocked:
		return "This code is locked after too many wrong tries. Create a new one on your Hub with “vaultsync-hub code”."
	case pairing.KindNoCode:
		return "Your Hub has no active code. Create one on your Hub with “vaultsync-hub code”."
	case pairing.KindRateLimited:
		return "Too many tries from this computer — wait a minute, then try again."
	case pairing.KindBusy:
		return "Your Hub is busy pairing other devices. Try again in a moment."
	case pairing.KindNotLocal:
		return "Your Hub pairs only on its local network. Connect this computer to the same network as your Hub."
	case pairing.KindIncompatible:
		return "This Hub uses a different version of pairing. Update VaultSync and your Hub, then try again."
	case pairing.KindSessionExpired:
		return "Pairing took too long. Run vaultsync pair again — your code may still be valid."
	case pairing.KindUnreachable:
		// macOS answers "no route to host" at once when vaultsync may not use
		// the local network. The decision is made for the program the
		// background service runs, and it holds for the same program in
		// Terminal too.
		if s.env.goos == "darwin" && errors.Is(err, syscall.EHOSTUNREACH) {
			return fmt.Sprintf("VaultSync could not reach your Hub at %s (“no route to host”). On a Mac this usually means vaultsync may not use your local network: allow “vaultsync” in %s, then try again.", hostOf(s.hub.Address), localNetworkSetting)
		}
		if s.opts.hub != "" {
			return fmt.Sprintf("Your Hub did not answer at %s. Check that it is running and on the same network as this computer.", s.hub.Address)
		}
		return "Your Hub did not answer. Check that it is running and on the same network as this computer."
	}
	return fmt.Sprintf("Pairing with your Hub failed: %v", err)
}

// macLocalNetworkNote is the possible cause a Mac adds when the search for a
// Hub came back empty or failed: macOS may keep vaultsync off the local
// network, and its search then finds nothing.
func macLocalNetworkNote(goos string) string {
	if goos != "darwin" {
		return ""
	}
	return " On a Mac, also check that “vaultsync” may use your local network: " + localNetworkSetting + "."
}

// cannotRead is the refusal for a folder VaultSync could not read. On a Mac,
// "operation not permitted" on a folder in Documents, Desktop or Downloads is
// macOS privacy protection, not the folder's permissions: the app vaultsync
// runs in needs access under Files & Folders.
func cannotRead(goos, home, path string, err error) error {
	if goos == "darwin" && errors.Is(err, syscall.EPERM) {
		return refuse("macOS did not let VaultSync read %s. Allow the app you run vaultsync in (Terminal, for example) to access this folder in System Settings → Privacy & Security → Files & Folders, then try again.", tildePath(home, path))
	}
	return refuse("VaultSync cannot read %s. Check its permissions or reconnect its disk, then try again.", tildePath(home, path))
}

// addHubDevice makes the Hub a known device of the engine, so its share can
// arrive. A Hub that was added before stays as it is (the person may have
// changed it); a new one also gets its LAN address as a hint, which speeds
// up the first connection where local discovery is blocked (for example by
// another Syncthing holding its port).
func (s *pairSession) addHubDevice(ctx context.Context) error {
	devices, err := s.env.engine.Devices(ctx)
	if err != nil {
		return fmt.Errorf("the sync engine does not answer: %w", err)
	}
	known := false
	for _, d := range devices {
		known = known || d.DeviceID == s.hello.HubDeviceID
	}
	if err := join.EnsureDevice(ctx, s.env.engine, s.hello.HubDeviceID, s.hubLabel()); err != nil {
		return fmt.Errorf("could not add your Hub to the sync engine: %w", err)
	}
	if known {
		return nil
	}
	hint := s.env.hubSyncAddress
	if hint == "" {
		hint = "tcp://" + net.JoinHostPort(hostOf(s.hub.Address), strconv.Itoa(defaultHubSyncPort))
	}
	return s.env.engine.PatchDevice(ctx, s.hello.HubDeviceID, map[string]any{"addresses": []string{"dynamic", hint}})
}

// --- provisioning -----------------------------------------------------------

// provision asks the Hub to share vault (or, with create, to start it). A
// request the Hub refused before acting, or that never left, is a definite
// failure; anything that broke after it may have left — a server error, a
// timeout, an answer that does not decrypt — is errOutcomeUnknown and never
// retried (decision 045). A session the Hub forgot is reconnected once with
// the code typed moments ago (that costs a start, not a wrong try, while the
// code is still valid).
func (s *pairSession) provision(ctx context.Context, vault string, create bool) (pairing.HubPayload, error) {
	for attempt := 0; ; attempt++ {
		if s.env.now().Sub(s.pairedAt) > hubSessionDeadline {
			if err := s.reconnect(ctx); err != nil {
				return pairing.HubPayload{}, err
			}
		}
		s.seq++
		pctx, cancel := context.WithTimeout(ctx, hubProvisionTimeout)
		reply, err := s.client.Provision(pctx, s.seq, s.myID, s.deviceName(), vault, create)
		cancel()
		if err == nil {
			return reply, nil
		}
		if pairing.FailureKind(err) == pairing.KindSessionExpired && attempt == 0 {
			if err := s.reconnect(ctx); err != nil {
				return pairing.HubPayload{}, err
			}
			continue
		}
		if provisionOutcomeUnknown(err) {
			return pairing.HubPayload{}, fmt.Errorf("%w: %v", errOutcomeUnknown, err)
		}
		return pairing.HubPayload{}, refuse("%s", handshakeText(err, s))
	}
}

func (s *pairSession) reconnect(ctx context.Context) error {
	s.t.say("  Pairing took too long. Reconnecting to %s with the code you entered…", quoted(s.hubLabel()))
	if err := s.handshake(ctx, s.code); err != nil {
		return refuse("%s", handshakeText(err, s))
	}
	if s.hello.Vaults == nil {
		return refuse("Your Hub could not list its vaults just now. Run vaultsync pair again in a moment.")
	}
	return nil
}

func provisionOutcomeUnknown(err error) bool {
	var apiErr *pairing.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status >= http.StatusInternalServerError
	}
	if errors.Is(err, pairing.ErrNotLocal) {
		return false
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return false
	}
	return true
}

func (s *pairSession) deviceName() string {
	if n := strings.TrimSpace(s.opts.name); n != "" {
		return pairing.SanitizeName(n)
	}
	return pairing.SanitizeName(s.env.deviceName)
}

// --- choosing the vault -----------------------------------------------------

type menuEntry struct {
	label string
	path  string             // a folder on this computer
	vault *pairing.VaultInfo // a vault on the Hub
	note  string
}

type vaultMenu struct {
	local, hub, already, unavailable []menuEntry
	registryNote                     string
}

func (s *pairSession) buildMenu(ctx context.Context) (vaultMenu, error) {
	var m vaultMenu
	folders, err := s.env.engine.Folders(ctx)
	if err != nil {
		return m, fmt.Errorf("the sync engine does not answer: %w", err)
	}
	configured := map[string]syncthing.FolderConfig{}
	listed := map[string]bool{} // engine folders already under "Already syncing"
	var enginePaths []string
	for _, f := range folders {
		configured[f.ID] = f
		enginePaths = append(enginePaths, f.Path)
	}
	vaults, errs := knownVaults(s.env.registries)
	if len(errs) > 0 {
		m.registryNote = "Obsidian’s list of vaults could not be read; vaults it lists may be missing here."
	}
	if len(vaults) == 0 && len(errs) == 0 {
		roots := s.env.cloud() // once per scan, not once per folder visited
		vaults = scanForVaults(s.env.scanRoots, func(p string) bool {
			_, blocked := cloudBlock(p, roots, s.env.goos)
			return blocked
		}, scanLimits{depth: 3, maxDirs: 4000, deadline: time.Now().Add(3 * time.Second)})
	}
	for _, v := range vaults {
		e := menuEntry{label: filepath.Base(v.Path), path: v.Path, note: tildePath(s.env.home, v.Path)}
		if !v.Opened.IsZero() {
			e.note += "  " + openedAgo(s.env.now(), v.Opened)
		}
		if st, err := os.Stat(v.Path); err != nil || !st.IsDir() {
			e.note = tildePath(s.env.home, v.Path) + " — not found: connect its disk, then look again"
			m.unavailable = append(m.unavailable, e)
			continue
		}
		if f, ok := sameConfigured(v.Path, folders, s.env.goos); ok {
			e.note = tildePath(s.env.home, f.Path)
			m.already = append(m.already, e)
			listed[f.ID] = true
			continue
		}
		if err := s.checkTarget(ctx, v.Path, filepath.Base(v.Path), enginePaths); err != nil {
			var r *refusal
			if errors.As(err, &r) {
				e.note = tildePath(s.env.home, v.Path) + " — " + shortReason(s, v.Path, enginePaths)
				m.unavailable = append(m.unavailable, e)
				continue
			}
			return m, err
		}
		m.local = append(m.local, e)
	}
	for i := range s.hello.Vaults {
		v := s.hello.Vaults[i]
		e := menuEntry{label: pairing.SanitizeName(v.Label), vault: &v}
		switch {
		case v.Files == 1:
			e.note = "1 file on your Hub"
		case v.Files > 1:
			e.note = fmt.Sprintf("%d files on your Hub", v.Files)
		}
		if f, ok := configured[v.ID]; ok {
			if listed[v.ID] {
				continue // already shown from Obsidian's list
			}
			e.note = tildePath(s.env.home, f.Path)
			m.already = append(m.already, e)
			continue
		}
		m.hub = append(m.hub, e)
	}
	return m, nil
}

// sameConfigured finds the engine folder that is exactly path.
func sameConfigured(path string, folders []syncthing.FolderConfig, goos string) (syncthing.FolderConfig, bool) {
	for _, f := range folders {
		for _, a := range resolvedForms(path) {
			for _, b := range resolvedForms(f.Path) {
				if foldCase(goos) && strings.EqualFold(a, b) || a == b {
					return f, true
				}
			}
		}
		if sameOrNested(path, f.Path) && sameOrNested(f.Path, path) {
			return f, true
		}
	}
	return syncthing.FolderConfig{}, false
}

func shortReason(s *pairSession, path string, enginePaths []string) string {
	if r, ok := cloudBlock(path, s.env.cloud(), s.env.goos); ok {
		return r.Provider + " — make a local copy first (A shows how)"
	}
	if _, ok := onDiskOverlap(path, s.reserved(), s.env.goos); ok {
		return "inside VaultSync’s own folder"
	}
	if us, ok := s.env.userST(); ok {
		if us.Unreadable != nil {
			return "the Syncthing on this computer could not be checked"
		}
		if _, ok := onDiskOverlap(path, us.Folders, s.env.goos); ok {
			return "synced by the Syncthing on this computer"
		}
	}
	if _, ok := onDiskOverlap(path, enginePaths, s.env.goos); ok {
		return "overlaps a folder VaultSync already syncs"
	}
	return "cannot sync"
}

func openedAgo(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < 24*time.Hour:
		return "opened today"
	case d < 48*time.Hour:
		return "opened yesterday"
	case d < 30*24*time.Hour:
		return fmt.Sprintf("opened %d days ago", int(d.Hours()/24))
	}
	return "opened " + t.Format("Jan 2006")
}

func (s *pairSession) choose(ctx context.Context) (plan, error) {
	if !s.t.interactive() || s.opts.vault != "" {
		return s.chooseFromFlags(ctx)
	}
	for {
		m, err := s.buildMenu(ctx)
		if err != nil {
			return plan{}, err
		}
		s.printMenu(m)
		a, err := s.t.ask("Choice: ")
		if err != nil {
			return plan{}, err
		}
		a = strings.ToUpper(strings.TrimSpace(a))
		var p plan
		switch a {
		case "Q":
			return plan{}, errCancelled
		case "R":
			if _, err := s.t.ask("  Open your vault in Obsidian once, then press Enter to look again."); err != nil {
				return plan{}, err
			}
			continue
		case "A":
			p, err = s.chooseOtherFolder(ctx)
		default:
			n, convErr := strconv.Atoi(a)
			if convErr != nil || n < 1 || n > len(m.local)+len(m.hub) {
				continue
			}
			if n <= len(m.local) {
				p, err = s.planForLocal(ctx, m.local[n-1].path)
			} else {
				p, err = s.planForHub(ctx, *m.hub[n-1-len(m.local)].vault)
			}
		}
		var r *refusal
		switch {
		case errors.As(err, &r):
			s.t.blank()
			s.t.say("%s", r.msg)
			s.t.blank()
			continue
		case errors.Is(err, errCancelled):
			continue // back to the menu
		case err != nil:
			return plan{}, err
		}
		return p, nil
	}
}

func (s *pairSession) printMenu(m vaultMenu) {
	s.t.blank()
	s.t.say("Choose a vault to sync")
	s.t.blank()
	n := 0
	s.t.say("On this computer")
	if len(m.local) == 0 {
		s.t.say("     (no Obsidian vault found here)")
	}
	for _, e := range m.local {
		n++
		s.t.say("  %d) %-14s %s", n, e.label, e.note)
	}
	if m.registryNote != "" {
		s.t.say("     %s", m.registryNote)
	}
	s.t.say("On your Hub")
	if len(m.hub) == 0 {
		s.t.say("     (nothing new on your Hub)")
	}
	for _, e := range m.hub {
		n++
		s.t.say("  %d) %-14s %s", n, e.label, e.note)
	}
	if len(m.already) > 0 {
		s.t.say("Already syncing")
		for _, e := range m.already {
			s.t.say("     %-14s %s", e.label, e.note)
		}
	}
	if len(m.unavailable) > 0 {
		s.t.say("Unavailable")
		for _, e := range m.unavailable {
			s.t.say("     %-14s %s", e.label, e.note)
		}
	}
	s.t.blank()
	s.t.say("  A) Another folder on this computer…")
	s.t.say("  R) Look again for local vaults")
	s.t.say("  Q) Cancel")
	s.t.blank()
}

func (s *pairSession) chooseOtherFolder(ctx context.Context) (plan, error) {
	a, err := s.t.ask("  Folder on this computer (Enter to go back): ")
	if err != nil || a == "" {
		return plan{}, errCancelled
	}
	path, err := expandPath(s.env.home, a)
	if err != nil {
		return plan{}, refuse("%s is not a folder path.", a)
	}
	if st, err := os.Stat(path); err != nil || !st.IsDir() {
		return plan{}, refuse("%s is not a folder on this computer. To download a vault from your Hub, choose it under On your Hub.", tildePath(s.env.home, path))
	}
	return s.planForLocal(ctx, path)
}

// planForLocal: a folder on this computer becomes a new vault on the Hub.
func (s *pairSession) planForLocal(ctx context.Context, path string) (plan, error) {
	enginePaths, err := s.enginePaths(ctx)
	if err != nil {
		return plan{}, err
	}
	path = resolveExistingPrefix(path)
	if err := s.checkTarget(ctx, path, filepath.Base(path), enginePaths); err != nil {
		return plan{}, err
	}
	// The folder is pinned before anything is asked: every answer below is
	// about this very folder.
	p := plan{kind: planNew, path: path}.withIdentity()
	if p.dir == nil {
		return plan{}, refuse("%s is not a folder on this computer.", tildePath(s.env.home, path))
	}
	_, empty, err := join.DirState(path)
	if err != nil {
		return plan{}, cannotRead(s.env.goos, s.env.home, path, err)
	}
	p.hadFiles = !empty
	if p.name, err = s.askHubName(filepath.Base(path), p.hadFiles); err != nil {
		return plan{}, err
	}
	if p.hadFiles {
		if err := s.askConsent(p.name, path); err != nil {
			return plan{}, err
		}
	}
	if err := s.stillTheSame(p); err != nil {
		return plan{}, err
	}
	return p, nil
}

// stillTheSame: the folder the person just answered about is the one that
// was looked at — same file identity (or, for a new one, the same place
// above it) and the same "empty or not".
func (s *pairSession) stillTheSame(p plan) error {
	changed := refuse("%s changed while you were answering. Nothing was changed — choose it again.", tildePath(s.env.home, p.path))
	if p.dir != nil {
		fi, err := os.Stat(p.path)
		if err != nil || !os.SameFile(fi, p.dir) {
			return changed
		}
	} else if err := s.checkAnchor(p); err != nil {
		return changed
	}
	exists, empty, err := join.DirState(p.path)
	if err != nil || (exists && !empty) != p.hadFiles {
		return changed
	}
	return nil
}

// askConsent is rule 7's explicit consent for a folder that holds files,
// naming the consequence; the default is No.
func (s *pairSession) askConsent(name, path string) error {
	for _, w := range syncPluginWarnings(path) {
		s.t.say("  %s", w)
	}
	s.t.blank()
	s.t.say("  %s at %s already contains files. VaultSync will sync this folder with %s.", quoted(name), tildePath(s.env.home, path), quoted(s.hubLabel()))
	s.t.say("  Edits and deletions on connected devices will also change this folder.")
	// Obsidian Sync cannot be detected from the vault (see syncPluginWarnings).
	s.t.say("  If Obsidian Sync or another service also syncs this folder, turn that off for it first.")
	ok, err := s.t.confirm("  Start syncing?")
	if err != nil {
		return err
	}
	if !ok {
		s.t.say("  Nothing was changed.")
		return errCancelled
	}
	return nil
}

// askHubName asks for the vault's name on the Hub. A name the Hub already
// uses is never reused for a folder that holds files: the Hub would share
// its existing vault instead of starting a new one.
func (s *pairSession) askHubName(def string, hasFiles bool) (string, error) {
	def = safeVaultName(def)
	for {
		a, err := s.t.ask(fmt.Sprintf("  Name on your Hub [%s]: ", def))
		if err != nil {
			return "", err
		}
		if a == "" {
			a = def
		}
		name := pairing.SanitizeName(a)
		switch {
		case name == "" || utf8.RuneCountInString(name) > maxHubNameRunes:
			s.t.say("  Use a name of 1 to %d characters.", maxHubNameRunes)
			continue
		case s.catalogueMatches(name) > 0:
			if hasFiles {
				s.t.say("  Your Hub already has a vault named %s. Both folders may contain files, so VaultSync cannot connect them. Choose a different name on your Hub — or, to download your Hub’s %s, choose it under On your Hub with a new, empty folder.", quoted(name), quoted(name))
			} else {
				s.t.say("  Your Hub already has a vault named %s. To download it, choose it under On your Hub; to start a new one, choose a different name.", quoted(name))
			}
			continue
		}
		return name, nil
	}
}

// catalogueMatches counts the catalogue vaults the Hub would resolve
// selector to: by ID, or by label without regard to case (findVaultByLabel).
func (s *pairSession) catalogueMatches(selector string) int {
	return catalogueMatchesIn(s.hello.Vaults, selector)
}

func catalogueMatchesIn(vaults []pairing.VaultInfo, selector string) int {
	n := 0
	for _, v := range vaults {
		if v.ID == selector || strings.EqualFold(strings.TrimSpace(v.Label), strings.TrimSpace(selector)) {
			n++
		}
	}
	return n
}

// planForHub: an existing Hub vault comes to a new or empty folder here.
func (s *pairSession) planForHub(ctx context.Context, v pairing.VaultInfo) (plan, error) {
	if catalogueMatchesIn(s.hello.Vaults, v.ID) != 1 {
		return plan{}, refuse("Your Hub has two vaults VaultSync cannot tell apart (%s). Rename one on your Hub, then pair again.", quoted(v.Label))
	}
	def := filepath.Join(s.env.home, "Vaults", safeVaultName(v.Label))
	for {
		a, err := s.t.ask(fmt.Sprintf("  Where should %s go on this computer? [%s]: ", quoted(pairing.SanitizeName(v.Label)), tildePath(s.env.home, def)))
		if err != nil {
			return plan{}, err
		}
		if a == "" {
			a = def
		}
		if strings.EqualFold(a, "q") {
			return plan{}, errCancelled
		}
		path, err := expandPath(s.env.home, a)
		if err != nil {
			s.t.say("  %s is not a folder path. Type another folder, or Q to go back.", a)
			continue
		}
		p, err := s.downloadPlan(ctx, v, path)
		var r *refusal
		if errors.As(err, &r) {
			s.t.say("  %s Type another folder, or Q to go back.", r.msg)
			continue
		}
		return p, err
	}
}

// downloadPlan checks a destination for an existing Hub vault: new or empty,
// below an existing folder (VaultSync creates ~/Vaults, nothing else — a
// missing parent can be a disk that is not connected).
func (s *pairSession) downloadPlan(ctx context.Context, v pairing.VaultInfo, path string) (plan, error) {
	folders, err := s.env.engine.Folders(ctx)
	if err != nil {
		return plan{}, fmt.Errorf("the sync engine does not answer: %w", err)
	}
	path = resolveExistingPrefix(path)
	// This vault may already sync on this computer — the overlap check below
	// would otherwise report the folder as overlapping itself (#228). At
	// this very folder there is nothing to do; elsewhere, a vault has one
	// folder per computer. Neither asks the Hub.
	for _, f := range folders {
		if f.ID != v.ID {
			continue
		}
		if _, same := sameConfigured(path, []syncthing.FolderConfig{f}, s.env.goos); same {
			return plan{}, &alreadySyncing{label: v.Label, path: tildePath(s.env.home, f.Path)}
		}
		return plan{}, refuse("%s already syncs at %s on this computer. A vault has one folder here — nothing was changed.", quoted(v.Label), tildePath(s.env.home, f.Path))
	}
	enginePaths := make([]string, 0, len(folders))
	for _, f := range folders {
		enginePaths = append(enginePaths, f.Path)
	}
	if err := s.checkTarget(ctx, path, v.Label, enginePaths); err != nil {
		return plan{}, err
	}
	exists, empty, err := join.DirState(path)
	if err != nil {
		return plan{}, cannotRead(s.env.goos, s.env.home, path, err)
	}
	if exists && !empty {
		return plan{}, refuse("%s already holds files. VaultSync downloads a vault from your Hub only into a new or empty folder — choose another one.", tildePath(s.env.home, path))
	}
	parent := filepath.Dir(path)
	vaults := resolveExistingPrefix(filepath.Join(s.env.home, "Vaults"))
	if st, err := os.Stat(parent); (err != nil || !st.IsDir()) && parent != vaults {
		return plan{}, refuse("The folder above %s does not exist. Create it first, or choose another place.", tildePath(s.env.home, path))
	}
	return plan{kind: planDownload, vault: v, path: path}.withIdentity(), nil
}

func (s *pairSession) chooseFromFlags(ctx context.Context) (plan, error) {
	if s.opts.vault == "" || s.opts.path == "" {
		return plan{}, refuse("Pass --vault NAME and --path FOLDER (with --create for a new vault on your Hub) — there is no terminal to ask on.")
	}
	path, err := expandPath(s.env.home, s.opts.path)
	if err != nil {
		return plan{}, refuse("%s is not a folder path.", s.opts.path)
	}
	switch n := s.catalogueMatches(s.opts.vault); {
	case n > 1:
		return plan{}, refuse("Your Hub has more than one vault matching %s. Name it by its ID instead (vaultsync-hub vault list shows the IDs).", quoted(s.opts.vault))
	case n == 1:
		for _, v := range s.hello.Vaults {
			if v.ID == s.opts.vault || strings.EqualFold(strings.TrimSpace(v.Label), strings.TrimSpace(s.opts.vault)) {
				return s.downloadPlan(ctx, v, path)
			}
		}
	}
	if !s.opts.create {
		return plan{}, refuse("Your Hub has no vault named %s. Add --create to start it as a new vault.", quoted(s.opts.vault))
	}
	name := pairing.SanitizeName(s.opts.vault)
	if name == "" || utf8.RuneCountInString(name) > maxHubNameRunes {
		return plan{}, refuse("Use a vault name of 1 to %d characters.", maxHubNameRunes)
	}
	enginePaths, err := s.enginePaths(ctx)
	if err != nil {
		return plan{}, err
	}
	path = resolveExistingPrefix(path)
	if err := s.checkTarget(ctx, path, name, enginePaths); err != nil {
		return plan{}, err
	}
	exists, empty, err := join.DirState(path)
	if err != nil {
		return plan{}, cannotRead(s.env.goos, s.env.home, path, err)
	}
	if !exists {
		if st, err := os.Stat(filepath.Dir(path)); err != nil || !st.IsDir() {
			return plan{}, refuse("The folder above %s does not exist. Create it first, or choose another place.", tildePath(s.env.home, path))
		}
	}
	p := plan{kind: planNew, name: name, path: path, hadFiles: exists && !empty}.withIdentity()
	if p.hadFiles && !s.opts.yes {
		if !s.t.interactive() {
			return plan{}, refuse("%s already contains files. VaultSync would sync it with your Hub as the new vault %s, and edits and deletions on connected devices would also change this folder. Pass --yes to agree.", tildePath(s.env.home, path), quoted(name))
		}
		if err := s.askConsent(name, path); err != nil {
			return plan{}, err
		}
		if err := s.stillTheSame(p); err != nil {
			return plan{}, err
		}
	}
	return p, nil
}

// --- the checks -------------------------------------------------------------

func (s *pairSession) enginePaths(ctx context.Context) ([]string, error) {
	folders, err := s.env.engine.Folders(ctx)
	if err != nil {
		return nil, fmt.Errorf("the sync engine does not answer: %w", err)
	}
	out := make([]string, 0, len(folders))
	for _, f := range folders {
		out = append(out, f.Path)
	}
	return out, nil
}

// reserved are VaultSync's own places: a vault may be neither inside one nor
// around one — `uninstall --remove-data` deletes some of them, and the
// engine's keys must never sync.
func (s *pairSession) reserved() []string {
	out := []string{s.env.lay.Base}
	if s.env.lay.Logs != "" {
		out = append(out, s.env.lay.Logs)
	}
	if s.env.unitDir != "" {
		out = append(out, s.env.unitDir)
	}
	return out
}

// checkTarget runs every rule a folder must pass before it may sync, and
// again right before it is added.
func (s *pairSession) checkTarget(ctx context.Context, path, name string, enginePaths []string) error {
	home := s.env.home
	if r, ok := onDiskOverlap(path, s.reserved(), s.env.goos); ok {
		return refuse("VaultSync keeps its own files in %s. A vault can be neither inside that folder nor around it — choose another one.", tildePath(home, r))
	}
	if r, ok := cloudBlock(path, s.env.cloud(), s.env.goos); ok {
		return refuse("%s is in %s and cannot sync with VaultSync there. Cloud storage can replace local files with placeholders, putting notes at risk when another service syncs the same folder. Make a fully downloaded copy outside %s, for example in %s. Keep the original, open the local copy in Obsidian, then run vaultsync pair.",
			quoted(safeVaultName(name)), r.Provider, r.Provider, tildePath(home, filepath.Join(home, "Vaults", safeVaultName(name))))
	}
	if other, ok := onDiskOverlap(path, enginePaths, s.env.goos); ok {
		return refuse("%s overlaps %s, which VaultSync already syncs. Two synced folders must never overlap — choose a folder outside it.", tildePath(home, path), tildePath(home, other))
	}
	if us, ok := s.env.userST(); ok {
		if us.Unreadable != nil {
			return refuse("VaultSync could not read the settings of the Syncthing on this computer (%s), so it cannot rule out that it already syncs this folder. Nothing was changed.", tildePath(home, us.ConfigPath))
		}
		if other, ok := onDiskOverlap(path, us.Folders, s.env.goos); ok {
			return refuse("%s is already synced by the Syncthing on this computer (%s). VaultSync never syncs a folder twice. To pair that Syncthing with your Hub instead, see docs/hub.md (vaultsync-hub pair).", tildePath(home, path), tildePath(home, other))
		}
	}
	return nil
}

// --- finishing --------------------------------------------------------------

func (s *pairSession) finish(ctx context.Context, p plan) error {
	label := p.label()
	att := pairAttempt{Hub: s.hubLabel(), HubID: s.hello.HubDeviceID, Vault: label, Path: p.path, At: s.env.now()}
	var reply pairing.HubPayload
	var err error
	if p.kind == planDownload {
		att.VaultID = p.vault.ID
		if err := s.recordAttempt(att, "sending"); err != nil {
			return err
		}
		reply, err = s.provision(ctx, p.vault.ID, false)
	} else {
		if p.hadFiles {
			// The name must still be free right before the Hub starts the
			// vault — a fresh catalogue, not the one from the handshake, and
			// one the Hub could actually read (null means it could not).
			fresh, ferr := s.provision(ctx, "", false)
			if ferr != nil || fresh.Vaults == nil || catalogueMatchesIn(fresh.Vaults, p.name) > 0 {
				return s.notNew(att, label)
			}
			s.hello.Vaults = fresh.Vaults
		}
		if err := s.recordAttempt(att, "sending"); err != nil {
			return err
		}
		reply, err = s.provision(ctx, p.name, true)
	}
	if errors.Is(err, errOutcomeUnknown) {
		_ = s.recordAttempt(att, "unknown")
		return refuse("VaultSync couldn’t confirm whether your Hub shared %s. Run vaultsync status to check before trying again.", quoted(label))
	}
	if err != nil {
		_ = s.recordAttempt(att, "refused")
		return err
	}
	if reply.Provisioned == nil {
		_ = s.recordAttempt(att, "refused")
		if strings.HasPrefix(reply.Error, pairing.RegistrationRefusedPrefix) {
			return refuse("Your Hub could not register this computer: %s. Check the Hub with “vaultsync-hub status”, then try again.", strings.TrimPrefix(reply.Error, pairing.RegistrationRefusedPrefix+": "))
		}
		return refuse("Your Hub could not share %s: %s. Check the Hub with “vaultsync-hub status”, then try again.", quoted(label), reply.Error)
	}
	vault := *reply.Provisioned
	att.VaultID = vault.ID
	if p.kind == planDownload && vault.ID != p.vault.ID {
		_ = s.recordAttempt(att, "refused")
		return refuse("Your Hub shared a different vault than the one you chose. Nothing was set up — pair again.")
	}
	if p.kind == planNew && !strings.EqualFold(strings.TrimSpace(vault.Label), strings.TrimSpace(p.name)) {
		_ = s.recordAttempt(att, "refused")
		return refuse("Your Hub shared %s instead of %s. Nothing was set up — pair again.", quoted(vault.Label), quoted(p.name))
	}
	if p.kind == planNew && p.hadFiles {
		if catalogueMatchesIn(s.hello.Vaults, vault.ID) > 0 || !s.provedNew(ctx, vault) {
			_ = s.recordAttempt(att, "refused")
			return refuse("VaultSync could not confirm that %s is a new, empty vault. Your local folder was not connected.", quoted(label))
		}
	}
	_ = s.recordAttempt(att, "shared")
	s.t.say("✓ Your Hub is sharing %s.", quoted(label))

	fmt.Fprint(s.t.out, "  Waiting for your Hub to reach this computer… ")
	if err := join.WaitForPendingFolder(ctx, s.env.engine, s.hello.HubDeviceID, vault.ID, s.env.pendingTimeout); err != nil {
		s.t.blank()
		if errors.Is(err, join.ErrShareDidNotArrive) {
			return refuse("Your Hub is sharing %s, but this computer has not received the share yet. This folder is not syncing. Check that both devices are online, then run vaultsync pair to finish setup.", quoted(label))
		}
		return err
	}
	s.t.say("ok")
	if err := s.accept(ctx, p, vault); err != nil {
		return err
	}
	_ = s.recordAttempt(att, "accepted")
	s.t.blank()
	s.t.say("✓ %s is set up to sync at %s. First sync is starting; check vaultsync status.", quoted(label), tildePath(s.env.home, p.path))
	if p.kind == planDownload {
		s.t.say("  Open it in Obsidian after the first sync: Open folder as vault → %s", tildePath(s.env.home, p.path))
	}
	return nil
}

// provedNew is the agent's evidence that a vault it asked the Hub to start
// is the new, empty one it asked for: absent from the catalogue right before
// (checked by the caller), a fresh catalogue now lists it as shared with this
// computer only, and the Hub reports no files (the shared guard checks that
// count again). The Hub cannot prove creation itself yet (#206).
func (s *pairSession) provedNew(ctx context.Context, v pairing.VaultInfo) bool {
	if v.Files != 0 {
		return false
	}
	reply, err := s.provision(ctx, "", false)
	if err != nil || reply.Vaults == nil {
		return false
	}
	for _, c := range reply.Vaults {
		if c.ID != v.ID {
			continue
		}
		for _, d := range c.SharedWith {
			if d != s.myID {
				return false
			}
		}
		return c.Files == 0
	}
	return false
}

func (s *pairSession) notNew(att pairAttempt, label string) error {
	_ = s.recordAttempt(att, "refused")
	return refuse("VaultSync could not confirm that %s would be a new, empty vault on your Hub. Your local folder was not connected.", quoted(label))
}

// accept hands the folder to the shared guard (join.AcceptShare) with the
// agent's own checks as its last step before the folder is added.
func (s *pairSession) accept(ctx context.Context, p plan, v pairing.VaultInfo) error {
	rep := &agentReporter{}
	err := join.AcceptShare(ctx, join.Local{
		Client:         s.env.engine,
		PendingTimeout: 5 * time.Second, // the offer is already there
		Reporter:       rep,
		Mkdir: func(path string) error {
			// Only a folder that did not exist when the person decided is
			// created — never a vault that vanished, never on a disk that
			// went away (its mount point would take the folder instead).
			if p.dir != nil {
				return refuse("%s disappeared while VaultSync was waiting for your Hub. Nothing was connected — reconnect its disk, then run vaultsync pair again.", tildePath(s.env.home, path))
			}
			if err := s.checkAnchor(p); err != nil {
				return err
			}
			return os.MkdirAll(path, 0o755)
		},
		BeforeAdd: func(abs string) error {
			return s.finalGate(ctx, p, v, abs)
		},
	}, s.hello.HubDeviceID, s.myID, v, p.path)
	if rep.alreadyAt != "" {
		return refuse("%s already syncs at %s.", quoted(v.Label), tildePath(s.env.home, rep.alreadyAt))
	}
	if err != nil && !isRefusal(err) {
		// An add whose answer was lost may still have happened: look.
		if folders, ferr := s.env.engine.Folders(ctx); ferr == nil {
			for _, f := range folders {
				if f.ID == v.ID {
					return nil
				}
			}
		}
	}
	return err
}

func isRefusal(err error) bool {
	var r *refusal
	return errors.As(err, &r) || errors.Is(err, join.ErrOverlap) || errors.Is(err, join.ErrMergeRefused) ||
		errors.Is(err, join.ErrHubCountUnknown) || errors.Is(err, join.ErrShareDidNotArrive)
}

// finalGate runs after the last wait, right before the folder is added: the
// engine is the one the checks ran against, the folder passes every rule
// again, and it still holds what the person decided on.
func (s *pairSession) finalGate(ctx context.Context, p plan, v pairing.VaultInfo, abs string) error {
	// The Hub side first — its request can take a while — so that every
	// local check below runs last, right before the folder is added: a
	// folder with files joins only a vault that is still new, empty and
	// shared with this computer alone (another device may have joined it
	// while this one waited).
	if p.kind == planNew && p.hadFiles && !s.provedNew(ctx, v) {
		return refuse("VaultSync could not confirm that %s is still a new, empty vault. Your local folder was not connected.", quoted(p.label()))
	}
	start, err := engineStartTime(ctx, s.env.engine)
	if err != nil || start != s.engineStart {
		return refuse("The sync engine restarted while VaultSync was setting up %s. Nothing was connected — run vaultsync pair again.", quoted(p.label()))
	}
	enginePaths, err := s.enginePaths(ctx)
	if err != nil {
		return err
	}
	// From here on only the file system is read — no request to wait for
	// between these checks and the add.
	// The very folder the person decided on — not another one put in its place.
	if p.dir != nil {
		if fi, err := os.Stat(abs); err != nil || !os.SameFile(fi, p.dir) {
			return refuse("%s was replaced while VaultSync was waiting for your Hub. Nothing was connected — run vaultsync pair again.", tildePath(s.env.home, abs))
		}
	} else if err := s.checkAnchor(p); err != nil {
		return err
	}
	if err := s.checkTarget(ctx, abs, p.label(), enginePaths); err != nil {
		return err
	}
	_, empty, err := join.DirState(abs)
	if err != nil {
		return cannotRead(s.env.goos, s.env.home, abs, err)
	}
	if empty == p.hadFiles {
		return refuse("%s changed while VaultSync was waiting for your Hub. Nothing was connected — run vaultsync pair again.", tildePath(s.env.home, abs))
	}
	return nil
}

// checkAnchor: the nearest existing folder above a new destination is still
// the one it was when the person decided (a disk that went away changes it).
func (s *pairSession) checkAnchor(p plan) error {
	if p.anchor == nil {
		return refuse("The place for %s cannot be checked. Nothing was connected.", tildePath(s.env.home, p.path))
	}
	if fi, err := os.Stat(p.anchorPath); err != nil || !os.SameFile(fi, p.anchor) {
		return refuse("The place for %s changed while VaultSync was waiting (a disk was disconnected?). Nothing was connected — run vaultsync pair again.", tildePath(s.env.home, p.path))
	}
	return nil
}

// agentReporter keeps the guard's progress quiet: the agent words it itself.
type agentReporter struct{ alreadyAt string }

func (r *agentReporter) AlreadyConfigured(path string) { r.alreadyAt = path }
func (r *agentReporter) Waiting()                      {}
func (r *agentReporter) WaitDone(error)                {}
func (r *agentReporter) Added(string, string)          {}

// engineStartTime identifies the running engine: a restart changes it.
func engineStartTime(ctx context.Context, c *syncthing.Client) (string, error) {
	var st struct {
		StartTime string `json:"startTime"`
	}
	if err := c.Get(ctx, "/rest/system/status", &st); err != nil {
		return "", err
	}
	if st.StartTime == "" {
		return "", errors.New("the sync engine reported no start time")
	}
	return st.StartTime, nil
}

// --- shares waiting to be set up ---------------------------------------------

// resumePending offers to finish a share a paired Hub already sends but this
// computer has not set up — without a code: it only goes into a new or empty
// folder (its file count is unknown, so the shared guard refuses anything
// else).
func (s *pairSession) resumePending(ctx context.Context) (bool, error) {
	if !s.t.interactive() || s.opts.code != "" || s.opts.vault != "" {
		return false, nil
	}
	waiting, err := pendingFromHubs(ctx, s.env.engine, s.myID)
	if err != nil || len(waiting) == 0 {
		return false, nil
	}
	for _, w := range waiting {
		s.t.say("Your Hub %s is sharing %s with this computer, but it is not set up here yet.", quoted(w.hub), quoted(w.label))
		ok, err := s.t.confirm("Set it up now?")
		if err != nil {
			return false, err
		}
		if !ok {
			continue
		}
		s.hello = pairing.HubPayload{HubDeviceID: w.hubID, HubName: w.hub}
		v := pairing.VaultInfo{ID: w.folderID, Label: w.label, Files: -1}
		s.hello.Vaults = []pairing.VaultInfo{v}
		p, err := s.planForHub(ctx, v)
		if err != nil {
			return false, err
		}
		if err := s.accept(ctx, p, v); err != nil {
			return true, err
		}
		s.t.blank()
		s.t.say("✓ %s is set up to sync at %s. First sync is starting; check vaultsync status.", quoted(w.label), tildePath(s.env.home, p.path))
		s.t.say("  Open it in Obsidian after the first sync: Open folder as vault → %s", tildePath(s.env.home, p.path))
		return true, nil
	}
	return false, nil
}

type pendingShare struct {
	folderID, label, hubID, hub string
}

// pendingFromHubs lists folders a paired Hub offers that are not set up here.
func pendingFromHubs(ctx context.Context, c *syncthing.Client, myID string) ([]pendingShare, error) {
	offers, err := c.PendingOffers(ctx)
	if err != nil {
		return nil, err
	}
	devices, err := c.Devices(ctx)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, d := range devices {
		if d.DeviceID != myID {
			names[d.DeviceID] = d.Name
		}
	}
	var out []pendingShare
	for id, by := range offers {
		for dev, offer := range by {
			name, known := names[dev]
			if !known {
				continue
			}
			label := pairing.SanitizeName(offer.Label)
			if label == "" {
				label = id
			}
			out = append(out, pendingShare{folderID: id, label: label, hubID: dev, hub: pairing.SanitizeName(name)})
		}
	}
	return out, nil
}

// --- small helpers ----------------------------------------------------------

// safeVaultName turns a vault label into one folder name: no separators,
// control characters or leading dots, at most 64 characters.
func safeVaultName(label string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(label) {
		switch {
		case r == '/' || r == '\\' || r == ':' || unicode.IsControl(r):
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	name := strings.Join(strings.Fields(b.String()), " ")
	name = strings.TrimLeft(name, ". ")
	for utf8.RuneCountInString(name) > maxHubNameRunes {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	if name == "" {
		return "Vault"
	}
	return name
}

// syncPluginWarnings names another sync service enabled in a vault: enabled
// is not proof that it syncs this vault, so it is a warning, not a block.
func syncPluginWarnings(vault string) []string {
	// Obsidian Sync is not detected: Obsidian turns its core Sync plugin on
	// in every new vault and keeps the connection to a remote vault outside
	// the vault folder, so nothing in .obsidian tells whether it syncs — a
	// warning from core-plugins.json would greet every vault (#175).
	var found []string
	plugins := map[string]string{
		"remotely-save":     "Remotely Save",
		"obsidian-livesync": "Self-hosted LiveSync",
		"obsidian-git":      "Obsidian Git",
	}
	if data, err := readSmallFile(filepath.Join(vault, ".obsidian", "community-plugins.json")); err == nil {
		var list []string
		if json.Unmarshal(data, &list) == nil {
			for _, p := range list {
				if name, ok := plugins[p]; ok {
					found = append(found, name)
				}
			}
		}
	}
	var out []string
	name := quoted(filepath.Base(vault))
	for _, f := range found {
		out = append(out, fmt.Sprintf("%s has %s enabled. Check whether it is syncing this vault, and turn that syncing off before using VaultSync.", name, f))
	}
	return out
}

// computerName is how this computer appears on the Hub.
func computerName(goos string) string {
	if goos == "darwin" {
		if out, err := exec.Command("scutil", "--get", "ComputerName").Output(); err == nil {
			if n := strings.TrimSpace(string(out)); n != "" {
				return n
			}
		}
	}
	h, _ := os.Hostname()
	return strings.TrimSuffix(strings.TrimSuffix(h, ".local"), ".lan")
}
