package tray

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// What the desktop does for the menu-bar app: opening a folder or a file
// in the user's own programs, showing a notification, running a dialog
// program. Each is one program run with its arguments — no shell.

// execRunner runs a program with stdin and returns what it printed and
// its exit status; a program that could not be started is the error.
func execRunner(ctx context.Context, name string, args []string, stdin string) (string, string, int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	// A program that hands its pipes to a child (xdg-open to a file
	// manager) would keep Run waiting until that child closes them; once
	// the program itself is gone, its output is taken as it is.
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	exit := -1
	if cmd.ProcessState != nil {
		exit = cmd.ProcessState.ExitCode()
	}
	status, err := outcome(err, ctx.Err(), exit)
	return out.String(), errb.String(), status, err
}

// outcome reads what a program's run came to: its exit status when it
// ended on its own — also when a child it left behind still held its
// pipes (xdg-open's file manager; exec.ErrWaitDelay) — the context's
// error when the context killed it (a time limit, a quit; the exit code
// is -1 then), and the run's own error when it could not run at all.
func outcome(runErr, ctxErr error, exitCode int) (status int, err error) {
	if runErr != nil && ctxErr != nil && exitCode == -1 {
		return 0, ctxErr
	}
	if errors.Is(runErr, exec.ErrWaitDelay) {
		return exitCode, nil
	}
	var ee *exec.ExitError
	if errors.As(runErr, &ee) {
		return ee.ExitCode(), nil
	}
	return 0, runErr
}

// openCommand is the program that opens path with the desktop: open on a
// Mac, xdg-open on Linux. Only an absolute path is opened — the paths
// come from the agent's report, and a relative one would be resolved
// against wherever this process happens to run. look says whether the
// path is a folder, or why it could not tell.
func openCommand(goos, path string, look func(string) (bool, error)) (name string, args []string, err error) {
	if path == "" || !filepath.IsAbs(path) {
		return "", nil, errors.New("not an absolute path")
	}
	switch goos {
	case "darwin":
		// A folder whose name has an extension can be a bundle to
		// LaunchServices (Journal.app), which `open` would launch — with
		// synced content that carries no quarantine. Such a folder is
		// revealed in its parent instead, which launches nothing; so is
		// one this app may not look at, which cannot be told from a
		// bundle (#255). Only a look that found a file (the log), or
		// nothing at all, opens it.
		if strings.Contains(strings.TrimPrefix(filepath.Base(path), "."), ".") {
			if dir, err := look(path); dir || (err != nil && !errors.Is(err, fs.ErrNotExist)) {
				return "open", []string{"-R", path}, nil
			}
		}
		return "open", []string{path}, nil
	case "linux":
		return "xdg-open", []string{path}, nil
	}
	return "", nil, errors.New("no program to open it with on " + goos)
}

// What the menu says when macOS stands between it and a folder (#255): it
// asks once whether VaultSync for Mac may open folders in Documents,
// Desktop or Downloads, and may be told no.
var (
	errOpenWaited  = errors.New("macOS may be asking whether “VaultSync for Mac” may open folders in Documents, Desktop or Downloads — answer that question, then click it again")
	errOpenRefused = errors.New("macOS has not allowed “VaultSync for Mac” to open it — allow it in System Settings → Privacy & Security → Files & Folders (“VaultSync for Mac”), then click it again")
)

// Open opens a folder or a file with the desktop's own program. What it
// opens outlives the menu-bar app: the program is not tied to ctx.
func Open(goos string) func(ctx context.Context, path string) error {
	return openWith(goos, execRunner, lookAt, readContents)
}

// openWith opens path with run; look says whether it is a folder (for the
// reveal of a dotted name), read reads its contents — the read macOS's Files
// & Folders refuses where a look passes.
func openWith(goos string, run runner, look func(string) (bool, error), read func(string) error) func(ctx context.Context, path string) error {
	return func(_ context.Context, path string) error {
		name, args, err := openCommand(goos, path, look)
		if err != nil {
			return err
		}
		// An opener that stays for as long as what it opened (a file
		// manager that does not go to the background) is left after half
		// a minute — the window it opened is its own process and stays.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, stderr, status, err := run(ctx, name, args, "")
		if errors.Is(err, context.DeadlineExceeded) {
			if goos == "darwin" {
				// open hands the folder to Finder and returns: one still
				// running after half a minute waits for macOS's question,
				// and the folder no longer opens when it is answered.
				return errOpenWaited
			}
			return nil
		}
		if err != nil {
			return err
		}
		if status != 0 {
			why := strings.TrimSpace(stderr)
			if why == "" {
				why = fmt.Sprintf("%s exited with status %d", name, status)
			}
			// An open that failed while macOS refuses this app the folder
			// names the Files & Folders setting, beside open's own words:
			// under that refusal a look at it (stat) still passes, its
			// contents are refused with EPERM. A plain permission (EACCES)
			// is not that setting's, and keeps open's words alone. (The
			// refusal alone does not stop an open: macOS 27.2 hands the
			// folder to the Finder all the same.)
			if goos == "darwin" && errors.Is(read(path), syscall.EPERM) {
				return fmt.Errorf("%w (%s)", errOpenRefused, why)
			}
			return errors.New(why)
		}
		return nil
	}
}

// readContents reads the first name in the folder at path — the read
// macOS's Files & Folders refuses where a look passes. Anything but a
// folder is not read: a vault is one, and opening a pipe to read it would
// wait for a writer.
func readContents(path string) error {
	fi, err := os.Stat(path)
	if err != nil || !fi.IsDir() {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Readdirnames(1); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// lookAt says whether path is a folder — or why it could not tell: a
// path that is not there, or one macOS does not let this app look at.
func lookAt(path string) (bool, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return fi.IsDir(), nil
}

// notifyCommand is the program that shows a notification: osascript on a
// Mac, notify-send on Linux when it is there.
func notifyCommand(goos string, lookPath func(string) (string, error), text string) (name string, args []string, stdin string, ok bool) {
	switch goos {
	case "darwin":
		return "osascript", nil, `display notification ` + appleScriptString(text) + ` with title ` + appleScriptString(dialogTitle), true
	case "linux":
		if _, err := lookPath("notify-send"); err == nil {
			return "notify-send", []string{"--", dialogTitle, text}, "", true
		}
	}
	return "", nil, "", false
}

// Notify shows a notification, or does nothing where none can be shown;
// what it shows is also in the menu, so a failure is not reported. It
// never holds the caller: the program runs off the loop, for ten seconds
// at most (a notification service that does not answer must not stall
// the menu).
func Notify(goos string, lookPath func(string) (string, error)) func(ctx context.Context, text string) {
	return func(_ context.Context, text string) {
		name, args, stdin, ok := notifyCommand(goos, lookPath, text)
		if !ok {
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, _, _, _ = execRunner(ctx, name, args, stdin)
		}()
	}
}
