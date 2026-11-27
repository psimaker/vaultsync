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

	layoutpkg "github.com/psimaker/vaultsync/desktop/internal/layout"
)

// errNoTerminal means a question needs an answer and nobody can give one.
var errNoTerminal = errors.New("no terminal to ask on")

// errInterrupted: Ctrl-C (or a stop) came while a question waited for its
// answer. It ends the command — it is never an answer, and never the "go
// back" an empty answer can be (#380, decision 081).
var errInterrupted = errors.New("interrupted")

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

// ask prints prompt and returns the trimmed answer. It returns
// errInterrupted as soon as ctx ends — the command's context, which Ctrl-C
// cancels — rather than wait for Return: a read of the terminal does not
// look at ctx (#380).
func (t *term) ask(ctx context.Context, prompt string) (string, error) {
	if t.in == nil {
		return "", errNoTerminal
	}
	if ctx.Err() != nil {
		return "", errInterrupted
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
	select {
	case r := <-read:
		if r.err != nil && (!errors.Is(r.err, io.EOF) || r.line == "") {
			return "", r.err
		}
		return strings.TrimSpace(r.line), nil
	case <-ctx.Done():
		t.reading = read
		// The terminal shows ^C after the prompt; what follows starts on a
		// line of its own.
		fmt.Fprintln(t.out)
		return "", errInterrupted
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
