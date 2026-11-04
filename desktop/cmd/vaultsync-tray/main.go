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
		fmt.Fprintln(os.Stderr, "error:", redact(err.Error()))
		os.Exit(1)
	}
}

// shown are the places a printed error names, shortened as the agent
// shortens them (stderr may be a log others read).
var shown struct{ folder, home string }

func redact(msg string) string { return layout.Shorten(msg, shown.folder, shown.home) }

// lockAtStart takes the one-instance lock before the icon shows: a
// second instance ends here (TakeLock has looked twice). Any other
// failure (the folder unreadable for a moment, something left at
// tray.lock) is left to the controller, which tries again at every
// refresh, logs it once and says so in the menu — as it does for the
// same failure later on.
func lockAtStart(lay layout.Layout) (tray.LockHandle, error) {
	held, err := tray.TakeLock(lay)
	if errors.Is(err, tray.ErrAlreadyRunning) {
		return nil, err
	}
	if err != nil {
		return nil, nil
	}
	return held, nil
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
	shown.home = home
	lay, err := layout.For(runtime.GOOS, home, os.Getenv)
	if err != nil {
		return err
	}
	if lay, err = lay.AtStateDir(*stateDir); err != nil {
		return err
	}
	shown.folder = lay.Base
	logger := log.New(os.Stderr, "", log.LstdFlags)
	// The lock is the controller's from here: it releases it on its way
	// out, or earlier when the file is gone and it takes a fresh one.
	held, err := lockAtStart(lay)
	if errors.Is(err, tray.ErrAlreadyRunning) {
		// Not a failure: the icon is up, or VaultSync is being removed. The
		// start at login keeps the app alive only after an unsuccessful
		// exit (decision 053), so a second start — a login item of the
		// person's own beside it — ends with 0 rather than being started
		// again every ten seconds.
		logger.Print(redact(err.Error()))
		return nil
	}
	if err != nil {
		return err
	}
	hint, err := missingWatcherHint()
	if err != nil {
		return err
	}
	if hint != "" {
		// A login item has no stderr anyone reads: the hint goes to the
		// desktop's notifications as well, where there are any.
		logger.Print(hint)
		tray.Notify(runtime.GOOS, exec.LookPath)(context.Background(), hint)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ui := newMenu()
	ui.onQuit = stop
	c := &tray.Controller{
		Home:   home,
		Layout: lay,
		UI:     ui,
		Asker:  tray.NewAsker(runtime.GOOS, home, exec.LookPath),
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
	return endOfRun(<-done, logger)
}

// endOfRun is what the app's run ends with, as its exit: Quit and the
// system ending the app (a cancelled run) are no failure, and neither is
// another instance taking the lock while this one ran (the lock's file
// went and came back) — the icon is up, and the start at login answers
// only a failure with a restart (decision 053). Anything else is one.
func endOfRun(runErr error, logger *log.Logger) error {
	if errors.Is(runErr, context.Canceled) {
		return nil
	}
	if errors.Is(runErr, tray.ErrAlreadyRunning) {
		logger.Print(redact(runErr.Error()))
		return nil
	}
	return runErr
}
