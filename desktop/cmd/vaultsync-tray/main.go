// vaultsync-tray is VaultSync's menu-bar app for a Mac or a Linux desktop
// (#176, decision 048): an icon that shows what `vaultsync status` shows,
// a menu with the vaults (click to open), Pause/Resume, Pair a Vault…,
// Open Log and Quit. It asks the running agent on its control socket for
// everything and decides nothing itself.
//
//	vaultsync-tray                 show the icon until Quit
//	vaultsync-tray --state-dir D   the VaultSync folder the agent was set up with
//	vaultsync-tray --version
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"fyne.io/systray"
	"github.com/psimaker/vaultsync/desktop/internal/layout"
	"github.com/psimaker/vaultsync/desktop/tray"
)

// version is stamped by the release build.
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `vaultsync-tray — VaultSync's menu-bar icon

  vaultsync-tray                 show the icon until Quit
  vaultsync-tray --state-dir D   the VaultSync folder the agent was set up with
  vaultsync-tray --version
`)
}

func run(args []string) error {
	fs := flag.NewFlagSet("vaultsync-tray", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	stateDir := fs.String("state-dir", "", "")
	showVersion := fs.Bool("version", false, "")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage(os.Stdout)
			return nil
		}
		usage(os.Stderr)
		return err
	}
	if fs.NArg() > 0 {
		usage(os.Stderr)
		return fmt.Errorf("unexpected %q", fs.Arg(0))
	}
	if *showVersion {
		fmt.Printf("vaultsync-tray %s\n", version)
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	lay, err := layout.For(runtime.GOOS, home, os.Getenv)
	if err != nil {
		return err
	}
	if *stateDir != "" {
		if !filepath.IsAbs(*stateDir) {
			return errors.New("--state-dir must be an absolute path")
		}
		lay = lay.At(filepath.Clean(*stateDir))
	}
	held, err := tray.TakeLock(lay.Base)
	if err != nil {
		return err
	}
	if held != nil {
		defer held.Release()
	}
	logger := log.New(os.Stderr, "", log.LstdFlags)
	if hint := missingWatcherHint(); hint != "" {
		logger.Print(hint)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ui := newMenu()
	ui.onQuit = stop
	c := &tray.Controller{
		GOOS:   runtime.GOOS,
		Home:   home,
		Layout: lay,
		UI:     ui,
		Asker:  tray.NewAsker(runtime.GOOS, exec.LookPath),
		Open:   tray.Open(runtime.GOOS),
		Notify: tray.Notify(runtime.GOOS, exec.LookPath),
		Logf:   logger.Printf,
		Lock:   tray.TakeLock,
		Held:   held,
	}
	// The controller ends the tray (Quit after Run returns), and the tray
	// ends the controller: when the native loop stops on its own — the
	// system ending the app — its exit callback cancels ctx, and the result
	// is waited for, so nothing touches the menu after it is gone.
	done := make(chan error, 1)
	systray.Run(func() {
		ui.build()
		go func() {
			done <- c.Run(ctx)
			systray.Quit()
		}()
	}, stop)
	runErr := <-done
	if errors.Is(runErr, context.Canceled) {
		return nil
	}
	return runErr
}
