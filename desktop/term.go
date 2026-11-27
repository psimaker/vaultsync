package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	layoutpkg "github.com/psimaker/vaultsync/desktop/internal/layout"
)

// errNoTerminal means a question needs an answer and nobody can give one.
var errNoTerminal = errors.New("no terminal to ask on")

// errInterrupted: Ctrl-C came while a question waited for its answer. It
// ends the command, which says what stays and exits 130 — it is never an
// answer, and never the "go back" an empty answer can be (#380, decision
// 081).
var errInterrupted = errors.New("interrupted")

// errTerminated: a termination signal came while a question waited. It ends
// the command without the interrupt's words, exit 143.
var errTerminated = errors.New("terminated")

// questionEnded is what a question returns when the command's context
// ended: errInterrupted for Ctrl-C, errTerminated for a termination signal
// (signalContext names the signal), and the context's own error for any
// other end, as every other step of the command returns it.
func questionEnded(ctx context.Context) error {
	var s stoppedBy
	if errors.As(context.Cause(ctx), &s) {
		switch s.sig {
		case os.Interrupt:
			return errInterrupted
		case syscall.SIGTERM:
			return errTerminated
		}
	}
	return ctx.Err()
}

// term is the person at the terminal. Without one (in is nil) every
// question that needs an answer becomes a refusal naming the flag to pass.
type term struct {
	out io.Writer
	in  *bufio.Reader
	// reading is a read an interrupted question left waiting for its line:
	// the next question takes that line, so two reads never share in.
	reading chan readResult
}

type readResult struct {
	line string
	err  error
}

// ttyPath is the controlling terminal; a test points it at nothing.
var ttyPath = "/dev/tty"

// openTerm reads answers from the controlling terminal, so `curl … | sh`
// (stdin is the script) can still ask.
func openTerm(out io.Writer) (*term, func()) {
	f, err := os.OpenFile(ttyPath, os.O_RDWR, 0)
	if err != nil {
		return &term{out: out}, func() {}
	}
	return &term{out: out, in: bufio.NewReader(f)}, func() { f.Close() }
}

func (t *term) interactive() bool { return t.in != nil }

func (t *term) say(format string, a ...any) { fmt.Fprintf(t.out, format+"\n", a...) }

func (t *term) blank() { fmt.Fprintln(t.out) }

// ask prints prompt and returns the trimmed answer. It returns as soon as
// ctx ends — the command's context, which Ctrl-C cancels — rather than wait
// for Return: a read of the terminal does not look at ctx (#380). What it
// returns then is questionEnded's.
func (t *term) ask(ctx context.Context, prompt string) (string, error) {
	if t.in == nil {
		return "", errNoTerminal
	}
	if ctx.Err() != nil {
		return "", questionEnded(ctx)
	}
	fmt.Fprint(t.out, prompt)
	read := t.reading
	t.reading = nil
	if read == nil {
		read = make(chan readResult, 1)
		go func(in *bufio.Reader) {
			line, err := in.ReadString('\n')
			read <- readResult{line, err}
		}(t.in)
	}
	ended := func() (string, error) {
		err := questionEnded(ctx)
		if errors.Is(err, errInterrupted) {
			// The terminal shows ^C after the prompt; what follows starts on
			// a line of its own.
			fmt.Fprintln(t.out)
		}
		return "", err
	}
	select {
	case r := <-read:
		// A line and the end of ctx that come together: the end wins and
		// the line is dropped, whichever of the two the select took. That
		// is harmless: the command ends either way, and every question comes
		// before the Hub is asked to share or start a vault and before the
		// engine changes — an answer taken here would only meet the ended
		// context at the next step.
		if ctx.Err() != nil {
			return ended()
		}
		if r.err != nil && (!errors.Is(r.err, io.EOF) || r.line == "") {
			return "", r.err
		}
		return strings.TrimSpace(r.line), nil
	case <-ctx.Done():
		t.reading = read
		return ended()
	}
}

// confirm asks a yes/no question whose default is No: only y or yes agree.
func (t *term) confirm(ctx context.Context, prompt string) (bool, error) {
	a, err := t.ask(ctx, prompt+" [y/N]: ")
	if err != nil {
		return false, err
	}
	a = strings.ToLower(a)
	return a == "y" || a == "yes", nil
}

// tildePath shows a path below the home folder as ~/…, the way people know
// it — the shared layout's, so the menu-bar app shows paths the same way.
func tildePath(home, p string) string { return layoutpkg.TildePath(home, p) }

// expandPath turns what a person typed into an absolute, clean path.
func expandPath(home, p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errors.New("no folder given")
	}
	if p == "~" || strings.HasPrefix(p, "~"+string(filepath.Separator)) {
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

// quoted puts a name in the same typographic quotes the iPhone uses.
func quoted(s string) string { return "“" + s + "”" }
