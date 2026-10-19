package tray

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
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
	if err != nil && ctx.Err() != nil {
		// Killed by the context (a time limit, a quit): that is the news,
		// not the exit status the kill left. A program that finished on
		// its own just before the limit is what it did.
		return out.String(), errb.String(), 0, ctx.Err()
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		// The program is done; a child it left behind still holds its
		// pipes (xdg-open's file manager). What the program itself did is
		// its exit status.
		return out.String(), errb.String(), cmd.ProcessState.ExitCode(), nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out.String(), errb.String(), ee.ExitCode(), nil
	}
	return out.String(), errb.String(), 0, err
}

// openCommand is the program that opens path with the desktop: open on a
// Mac, xdg-open on Linux. Only an absolute path is opened — the paths
// come from the agent's report, and a relative one would be resolved
// against wherever this process happens to run.
func openCommand(goos, path string) (name string, args []string, err error) {
	if path == "" || !filepath.IsAbs(path) {
		return "", nil, errors.New("not an absolute path")
	}
	switch goos {
	case "darwin":
		return "open", []string{path}, nil
	case "linux":
		return "xdg-open", []string{path}, nil
	}
	return "", nil, errors.New("no program to open it with on " + goos)
}

// Open opens a folder or a file with the desktop's own program. What it
// opens outlives the menu-bar app: the program is not tied to ctx.
func Open(goos string) func(ctx context.Context, path string) error {
	return openWith(goos, execRunner)
}

func openWith(goos string, run runner) func(ctx context.Context, path string) error {
	return func(_ context.Context, path string) error {
		name, args, err := openCommand(goos, path)
		if err != nil {
			return err
		}
		_, stderr, status, err := run(context.Background(), name, args, "")
		if err != nil {
			return err
		}
		if status != 0 {
			if why := strings.TrimSpace(stderr); why != "" {
				return errors.New(why)
			}
			return fmt.Errorf("%s exited with status %d", name, status)
		}
		return nil
	}
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
