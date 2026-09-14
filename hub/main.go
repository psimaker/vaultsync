// vaultsync-hub is the coordinator of a VaultSync Hub: a self-hosted Syncthing
// instance that pairs Obsidian devices with a short code instead of device IDs
// and Web UIs.
//
// Hub side (inside the container, or on any host running the stack):
//
//	vaultsync-hub init                 apply the Hub defaults (idempotent)
//	vaultsync-hub serve                run discovery + pairing (the container's command)
//	vaultsync-hub code                 issue a new pairing code (24 h)
//	vaultsync-hub status               device ID, vaults, paired devices
//	vaultsync-hub vault list|create NAME|adopt NAME
//
// Device side (a computer with its own Syncthing):
//
//	vaultsync-hub pair --code WORD-WORD-NN [--vault NAME] [--create] [--path DIR] [--hub HOST:PORT]
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// version is stamped by the Dockerfile / release build.
var version = "dev"

const (
	defaultPort       = 8390
	defaultHubName    = "VaultSync Hub"
	pairingCodeTTL    = 24 * time.Hour
	syncthingWaitInit = 120 * time.Second
)

type config struct {
	apiURL      string // SYNCTHING_API_URL override; empty = from config.xml
	statePath   string
	vaultsRoot  string // as Syncthing sees it
	vaultsLocal string // as this process sees it
	port        int
	hubName     string
}

func loadConfig() (config, error) {
	c := config{
		apiURL:      strings.TrimSpace(os.Getenv("SYNCTHING_API_URL")),
		statePath:   strings.TrimSpace(os.Getenv("VAULTSYNC_HUB_STATE")),
		vaultsRoot:  strings.TrimSpace(os.Getenv("VAULTSYNC_HUB_VAULTS")),
		vaultsLocal: strings.TrimSpace(os.Getenv("VAULTSYNC_HUB_VAULTS_LOCAL")),
		port:        defaultPort,
		hubName:     strings.TrimSpace(os.Getenv("VAULTSYNC_HUB_NAME")),
	}
	if p := strings.TrimSpace(os.Getenv("VAULTSYNC_HUB_PORT")); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return c, fmt.Errorf("VAULTSYNC_HUB_PORT=%q is not a valid port", p)
		}
		c.port = n
	}
	if c.statePath == "" {
		c.statePath = defaultStatePath()
	}
	if c.vaultsRoot == "" {
		c.vaultsRoot = "/var/syncthing/vaults"
	}
	if c.vaultsLocal == "" {
		c.vaultsLocal = c.vaultsRoot
	}
	if c.hubName == "" {
		c.hubName = defaultHubName
	}
	return c, nil
}

func defaultStatePath() string {
	if runtime.GOOS == "linux" {
		if st, err := os.Stat("/var/lib/vaultsync-hub"); err == nil && st.IsDir() {
			return "/var/lib/vaultsync-hub/state.json"
		}
	}
	base := strings.TrimSpace(os.Getenv("XDG_STATE_HOME"))
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "vaultsync-hub", "state.json")
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return errors.New("a command is required")
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "version", "--version", "-v":
		fmt.Println("vaultsync-hub", version)
		return nil
	case "help", "--help", "-h":
		usage()
		return nil
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch cmd {
	case "init":
		return cmdInit(ctx, cfg, rest)
	case "serve":
		return cmdServe(ctx, cfg, rest)
	case "code":
		return cmdCode(ctx, cfg, rest)
	case "status":
		return cmdStatus(ctx, cfg)
	case "vault":
		return cmdVault(ctx, cfg, rest)
	case "pair":
		return cmdPair(ctx, cfg, rest)
	default:
		usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `vaultsync-hub — VaultSync Hub coordinator

Hub commands:
  init                       apply the Hub defaults to Syncthing (safe to repeat)
  serve                      run LAN discovery and the pairing service
  code                       issue a new pairing code (valid 24 hours)
  status                     show device ID, vaults and paired devices
  vault list                 list vaults
  vault create NAME          create an empty vault
  vault adopt NAME           turn an existing directory under the vaults root into a vault

Device commands (this computer runs its own Syncthing):
  pair --code WORD-WORD-NN   pair with a Hub on the same network
       [--vault NAME]        vault to join (or create with --create)
       [--path DIR]          local directory for the vault (accepts the share)
       [--hub HOST:PORT]     skip discovery
       [--name NAME]         how this device appears on the Hub

Environment: SYNCTHING_CONFIG, SYNCTHING_API_URL, VAULTSYNC_HUB_STATE,
             VAULTSYNC_HUB_VAULTS, VAULTSYNC_HUB_PORT, VAULTSYNC_HUB_NAME
`)
}

func connect(ctx context.Context, cfg config, wait time.Duration) (*SyncthingClient, error) {
	client, det, err := waitForSyncthing(ctx, cfg.apiURL, wait, func(msg string) { fmt.Println(msg) })
	if err != nil {
		return nil, err
	}
	_ = det
	return client, nil
}

// --- init -------------------------------------------------------------------

func cmdInit(ctx context.Context, cfg config, args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	keepName := fs.Bool("keep-name", false, "do not rename the Syncthing device")
	if err := fs.Parse(args); err != nil {
		return err
	}
	client, err := connect(ctx, cfg, syncthingWaitInit)
	if err != nil {
		return err
	}
	myID, err := applyHubDefaults(ctx, client, cfg.hubName, !*keepName)
	if err != nil {
		return err
	}
	fmt.Println("Hub is configured.")
	fmt.Println("Device ID:", myID)
	return nil
}

// applyHubDefaults makes the Syncthing instance behave like a Hub. Every step
// is idempotent; nothing here touches folders or devices that already exist.
func applyHubDefaults(ctx context.Context, client *SyncthingClient, hubName string, rename bool) (string, error) {
	myID, err := client.MyID(ctx)
	if err != nil {
		return "", err
	}
	if rename {
		if err := client.PatchDevice(ctx, myID, map[string]any{"name": hubName}); err != nil {
			return "", fmt.Errorf("set device name: %w", err)
		}
	}
	opts, err := client.Options(ctx)
	if err != nil {
		return "", err
	}
	patch := map[string]any{
		"startBrowser":          false,
		"crashReportingEnabled": false,
		"globalAnnounceEnabled": true,
		"localAnnounceEnabled":  true,
		"relaysEnabled":         true,
		"natEnabled":            true,
	}
	// Usage reporting: decline only while the operator has not decided (0);
	// an explicit accept (>0) or decline (-1) stays as it is.
	if v, ok := opts["urAccepted"].(float64); ok && v == 0 {
		patch["urAccepted"] = -1
	}
	if err := client.PatchOptions(ctx, patch); err != nil {
		return "", fmt.Errorf("set options: %w", err)
	}
	if err := client.PatchFolderDefaults(ctx, map[string]any{
		"type":             "sendreceive",
		"rescanIntervalS":  hubRescanIntervalS,
		"fsWatcherEnabled": true,
		"fsWatcherDelayS":  hubFSWatcherDelayS,
		"ignorePerms":      true,
		"autoNormalize":    true,
		"maxConflicts":     hubMaxConflicts,
		"versioning":       hubVersioning(),
	}); err != nil {
		return "", fmt.Errorf("set folder defaults: %w", err)
	}
	return myID, nil
}

// --- serve ------------------------------------------------------------------

func cmdServe(ctx context.Context, cfg config, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	listen := fs.String("listen", "", "pairing listen address (default: all interfaces on VAULTSYNC_HUB_PORT)")
	noDiscovery := fs.Bool("no-discovery", false, "do not answer LAN discovery probes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	client, err := connect(ctx, cfg, syncthingWaitInit)
	if err != nil {
		return err
	}
	if _, err := applyHubDefaults(ctx, client, cfg.hubName, true); err != nil {
		return err
	}
	store := newStateStore(cfg.statePath)
	if _, err := store.load(); err != nil {
		return err
	}
	prov := newProvisioner(client, cfg.vaultsRoot, cfg.vaultsLocal)
	srv := newPairingServer(store, prov, cfg.hubName, version)
	addr := *listen
	port := cfg.port
	if addr == "" {
		addr = ":" + strconv.Itoa(port)
	} else {
		// Discovery must advertise the port pairing actually listens on.
		_, p, err := net.SplitHostPort(addr)
		if err != nil {
			return fmt.Errorf("--listen must be host:port: %w", err)
		}
		if port, err = strconv.Atoi(p); err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("--listen has an invalid port %q", p)
		}
	}
	errCh := make(chan error, 2)
	if !*noDiscovery {
		go func() { errCh <- serveDiscovery(ctx, port, cfg.hubName, srv.logf) }()
	}
	go func() { errCh <- servePairingHTTP(ctx, addr, srv) }()
	srv.logf("vaultsync-hub %s: pairing on %s, discovery on udp/%d", version, addr, port)
	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}

// --- code -------------------------------------------------------------------

func cmdCode(ctx context.Context, cfg config, args []string) error {
	fs := flag.NewFlagSet("code", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	code, err := generateCode()
	if err != nil {
		return err
	}
	store := newStateStore(cfg.statePath)
	now := time.Now()
	if _, err := store.update(func(st *hubState) error {
		st.Pairing = &pairingCodeState{
			Scalar:    passwordScalarForCode(code),
			CreatedAt: now,
			ExpiresAt: now.Add(pairingCodeTTL),
		}
		return nil
	}); err != nil {
		return err
	}
	fmt.Println()
	fmt.Println("  Pairing code (valid 24 hours, on this network only):")
	fmt.Println()
	fmt.Printf("      %s\n", code)
	fmt.Println()
	fmt.Println("  On the next device:  curl -fsSL https://vaultsync.eu/setup.sh | sh")
	fmt.Println("  In VaultSync on iPhone: Add Hub → enter this code")
	fmt.Println()
	_ = ctx
	return nil
}

// --- status -----------------------------------------------------------------

func cmdStatus(ctx context.Context, cfg config) error {
	client, err := connect(ctx, cfg, 10*time.Second)
	if err != nil {
		return err
	}
	myID, err := client.MyID(ctx)
	if err != nil {
		return err
	}
	prov := newProvisioner(client, cfg.vaultsRoot, cfg.vaultsLocal)
	vaults, err := prov.listVaults(ctx)
	if err != nil {
		return err
	}
	devices, err := client.Devices(ctx)
	if err != nil {
		return err
	}
	connected, _ := client.ConnectedDevices(ctx)
	st, err := newStateStore(cfg.statePath).load()
	if err != nil {
		return err
	}
	fmt.Println("VaultSync Hub", version)
	fmt.Println("Device ID:  ", myID)
	if p, err := st.activePairing(time.Now()); err == nil {
		fmt.Printf("Pairing:     open until %s\n", p.ExpiresAt.Local().Format("2006-01-02 15:04"))
	} else {
		fmt.Println("Pairing:     closed (run `vaultsync-hub code` to open)")
	}
	fmt.Println()
	fmt.Printf("Vaults (%d):\n", len(vaults))
	for _, v := range vaults {
		fmt.Printf("  %-32s %8d files   shared with %d device(s)\n", v.Label, v.Files, len(v.SharedWith))
	}
	fmt.Println()
	fmt.Printf("Devices (%d):\n", len(devices)-1)
	for _, d := range devices {
		if d.DeviceID == myID {
			continue
		}
		state := "offline"
		if connected[d.DeviceID] {
			state = "connected"
		}
		name := d.Name
		if name == "" {
			name = "(unnamed)"
		}
		fmt.Printf("  %-32s %s\n", name, state)
	}
	return nil
}

// --- vault ------------------------------------------------------------------

func cmdVault(ctx context.Context, cfg config, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: vault list | vault create NAME | vault adopt NAME")
	}
	client, err := connect(ctx, cfg, 10*time.Second)
	if err != nil {
		return err
	}
	prov := newProvisioner(client, cfg.vaultsRoot, cfg.vaultsLocal)
	switch args[0] {
	case "list":
		vaults, err := prov.listVaults(ctx)
		if err != nil {
			return err
		}
		for _, v := range vaults {
			fmt.Printf("%-32s %8d files   %s\n", v.Label, v.Files, v.ID)
		}
		return nil
	case "create", "adopt":
		if len(args) != 2 {
			return fmt.Errorf("usage: vault %s NAME", args[0])
		}
		f, err := prov.createVault(ctx, args[1], args[0] == "adopt")
		if err != nil {
			return err
		}
		fmt.Printf("Vault %q ready at %s (%s)\n", f.Label, f.Path, f.ID)
		return nil
	default:
		return fmt.Errorf("unknown vault command %q", args[0])
	}
}

// --- pair (device side) -----------------------------------------------------

func cmdPair(ctx context.Context, cfg config, args []string) error {
	fs := flag.NewFlagSet("pair", flag.ContinueOnError)
	codeFlag := fs.String("code", "", "pairing code shown by the Hub")
	hubAddr := fs.String("hub", "", "hub address host:port (skips discovery)")
	vault := fs.String("vault", "", "vault to join on the Hub")
	create := fs.Bool("create", false, "create the vault on the Hub if it does not exist")
	path := fs.String("path", "", "local directory for the vault; accepts the Hub's share into it")
	name := fs.String("name", "", "name of this device as shown on the Hub")
	if err := fs.Parse(args); err != nil {
		return err
	}
	code, err := normalizeCode(*codeFlag)
	if err != nil {
		return errors.New("--code must look like WORD-WORD-NN, exactly as the Hub printed it")
	}

	// Local Syncthing first: pairing without it has nothing to pair.
	local, det, err := waitForSyncthing(ctx, "", 5*time.Second, nil)
	if err != nil {
		return fmt.Errorf("no running Syncthing found on this computer (%w)", err)
	}
	myID, err := local.MyID(ctx)
	if err != nil {
		return err
	}
	deviceName := strings.TrimSpace(*name)
	if deviceName == "" {
		deviceName, _ = os.Hostname()
	}
	fmt.Printf("✓ Local Syncthing found (%s)\n", det.Source)

	candidates, err := hubCandidates(ctx, cfg.port, *hubAddr)
	if err != nil {
		return err
	}
	var client *pairClient
	var hello hubPayload
	for _, cand := range candidates {
		c := newPairClient(cand.Address)
		reply, err := c.handshake(ctx, code)
		if err == nil {
			client, hello = c, reply
			break
		}
		if !errors.Is(err, errCodeRejected) {
			fmt.Printf("  hub at %s did not answer properly: %v\n", cand.Address, err)
		}
	}
	if client == nil {
		return errors.New("no hub accepted this code — check the code, or issue a new one with `vaultsync-hub code` on the Hub")
	}
	fmt.Printf("✓ Connected to %s\n", hello.HubName)

	// The Hub must be a known device locally before its share can arrive.
	localProv := newProvisioner(local, "", "")
	if err := localProv.ensureDevice(ctx, hello.HubDeviceID, hello.HubName); err != nil {
		return fmt.Errorf("add the Hub to local Syncthing: %w", err)
	}

	chosen := strings.TrimSpace(*vault)
	if chosen == "" {
		chosen, *create, err = chooseVault(hello.Vaults)
		if err != nil {
			return err
		}
	}
	var seq uint64 = 1
	reply, err := client.provision(ctx, seq, myID, deviceName, chosen, *create)
	if err != nil {
		return err
	}
	if reply.Error != "" {
		return errors.New(reply.Error)
	}
	if reply.Provisioned == nil {
		return errors.New("the Hub did not share a vault")
	}
	fmt.Printf("✓ Hub shares vault %q with this device\n", reply.Provisioned.Label)

	if strings.TrimSpace(*path) == "" {
		fmt.Println()
		fmt.Println("Next: accept the shared folder in your Syncthing (it appears as a new share),")
		fmt.Println("or re-run with --path DIR to let this command accept it into DIR.")
		return nil
	}
	return acceptShareLocally(ctx, local, localProv, hello.HubDeviceID, myID, *reply.Provisioned, *path)
}

func hubCandidates(ctx context.Context, port int, explicit string) ([]discoveredHub, error) {
	if explicit != "" {
		if !strings.Contains(explicit, ":") {
			explicit = explicit + ":" + strconv.Itoa(port)
		}
		return []discoveredHub{{Address: explicit}}, nil
	}
	fmt.Print("  Looking for a Hub on this network… ")
	hubs, err := discoverHubs(ctx, port, 3*time.Second)
	if err != nil {
		fmt.Println()
		return nil, err
	}
	if len(hubs) == 0 {
		fmt.Println("none")
		return nil, errors.New("no Hub answered. Device and Hub must be on the same network for pairing; if they are, pass --hub HOST:PORT")
	}
	fmt.Printf("%d found\n", len(hubs))
	return hubs, nil
}

// chooseVault asks interactively; returns (name, create).
func chooseVault(vaults []vaultInfo) (string, bool, error) {
	in, err := openTTY()
	if err != nil {
		return "", false, errors.New("no terminal available — pass --vault NAME (and --create for a new vault)")
	}
	defer in.Close()
	reader := bufio.NewReader(in)
	fmt.Println()
	fmt.Println("Vaults on the Hub:")
	for i, v := range vaults {
		fmt.Printf("  %d) %s  (%d files)\n", i+1, v.Label, v.Files)
	}
	fmt.Printf("  %d) New vault\n", len(vaults)+1)
	for {
		fmt.Print("Choice: ")
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", false, err
		}
		n, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil || n < 1 || n > len(vaults)+1 {
			continue
		}
		if n <= len(vaults) {
			return vaults[n-1].Label, false, nil
		}
		fmt.Print("Name of the new vault: ")
		name, err := reader.ReadString('\n')
		if err != nil {
			return "", false, err
		}
		if _, err := slugify(name); err != nil {
			fmt.Println("  ", err)
			continue
		}
		return strings.TrimSpace(name), true, nil
	}
}

func openTTY() (*os.File, error) {
	if runtime.GOOS == "windows" {
		return os.Stdin, nil
	}
	return os.OpenFile("/dev/tty", os.O_RDONLY, 0)
}

// acceptShareLocally waits for the Hub's share to arrive as a pending folder
// and adds it with the given path. Safety: the path must be empty (or absent)
// unless the vault on the Hub is brand new, in which case existing local
// content becomes the first copy. Two non-empty sides never get merged here.
func acceptShareLocally(ctx context.Context, local *SyncthingClient, prov *provisioner, hubID, myID string, v vaultInfo, path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	folders, err := local.Folders(ctx)
	if err != nil {
		return err
	}
	for _, f := range folders {
		if f.ID == v.ID {
			fmt.Printf("✓ Vault already configured locally at %s\n", f.Path)
			return nil
		}
		if pathsOverlap(f.Path, abs) {
			return fmt.Errorf("%s overlaps the existing Syncthing folder %q — choose another directory", abs, f.Label)
		}
	}
	exists, empty, err := prov.dirState(abs)
	if err != nil {
		return err
	}
	if exists && !empty {
		// Merging two non-empty sides is the one operation VaultSync never
		// performs on its own. An unknown Hub file count (< 0) fails closed.
		if v.Files < 0 {
			return fmt.Errorf("%s already holds files and the Hub could not confirm that its vault is empty — nothing was changed. Use an empty directory, or retry", abs)
		}
		if v.Files > 0 {
			return fmt.Errorf("%s already holds files and the Hub's vault is not empty — nothing was changed. Move one of them aside; VaultSync never merges two vaults on its own", abs)
		}
	}
	if !exists {
		if err := prov.mkdir(abs); err != nil {
			return err
		}
	}
	fmt.Print("  Waiting for the share to arrive… ")
	if err := waitForPendingFolder(ctx, local, hubID, v.ID, 90*time.Second); err != nil {
		fmt.Println()
		return err
	}
	fmt.Println("ok")
	f := folderConfig{
		ID:               v.ID,
		Label:            v.Label,
		Path:             abs,
		Type:             "sendreceive",
		Devices:          []folderDevice{{DeviceID: myID}, {DeviceID: hubID}},
		RescanIntervalS:  hubRescanIntervalS,
		FSWatcherEnabled: true,
		FSWatcherDelayS:  hubFSWatcherDelayS,
		IgnorePerms:      true,
		AutoNormalize:    true,
		MaxConflicts:     hubMaxConflicts,
	}
	if err := local.AddFolder(ctx, f); err != nil {
		return err
	}
	fmt.Printf("✓ Vault %q syncs with the Hub at %s\n", v.Label, abs)
	return nil
}

func waitForPendingFolder(ctx context.Context, client *SyncthingClient, fromDevice, folderID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		pending, err := client.PendingFolders(ctx)
		if err == nil {
			if offers, ok := pending[folderID]; ok {
				for _, o := range offers {
					if o == fromDevice {
						return nil
					}
				}
			}
		}
		if time.Now().After(deadline) {
			return errors.New("the Hub's share did not arrive in time — is the Hub reachable from this computer? (Syncthing needs a few seconds to connect; re-run to retry)")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func passwordScalarForCode(code string) []byte {
	return pakePasswordScalar([]byte(code))
}

func servePairingHTTP(ctx context.Context, addr string, srv *pairingServer) error {
	return serveHTTP(ctx, addr, srv.handler())
}
