package tray

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
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
	err := cmd.Run()
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

// Open opens a folder or a file with the desktop's own program.
func Open(goos string) func(ctx context.Context, path string) error {
	return func(ctx context.Context, path string) error {
		name, args, err := openCommand(goos, path)
		if err != nil {
			return err
		}
		_, stderr, status, err := execRunner(ctx, name, args, "")
		if err != nil {
			return err
		}
		if status != 0 {
			return errors.New(strings.TrimSpace(stderr))
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
// what it shows is also in the menu, so a failure is not reported.
func Notify(goos string, lookPath func(string) (string, error)) func(ctx context.Context, text string) {
	return func(ctx context.Context, text string) {
		name, args, stdin, ok := notifyCommand(goos, lookPath, text)
		if !ok {
			return
		}
		_, _, _, _ = execRunner(ctx, name, args, stdin)
	}
}
