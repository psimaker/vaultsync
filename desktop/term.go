package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// errNoTerminal means a question needs an answer and nobody can give one.
var errNoTerminal = errors.New("no terminal to ask on")

// term is the person at the terminal. Without one (in is nil) every
// question that needs an answer becomes a refusal naming the flag to pass.
type term struct {
	out io.Writer
	in  *bufio.Reader
}

// openTerm reads answers from the controlling terminal, so `curl … | sh`
// (stdin is the script) can still ask.
func openTerm(out io.Writer) (*term, func()) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return &term{out: out}, func() {}
	}
	return &term{out: out, in: bufio.NewReader(f)}, func() { f.Close() }
}

func (t *term) interactive() bool { return t.in != nil }

func (t *term) say(format string, a ...any) { fmt.Fprintf(t.out, format+"\n", a...) }

func (t *term) blank() { fmt.Fprintln(t.out) }

// ask prints prompt and returns the trimmed answer.
func (t *term) ask(prompt string) (string, error) {
	if t.in == nil {
		return "", errNoTerminal
	}
	fmt.Fprint(t.out, prompt)
	line, err := t.in.ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// confirm asks a yes/no question whose default is No: only y or yes agree.
func (t *term) confirm(prompt string) (bool, error) {
	a, err := t.ask(prompt + " [y/N]: ")
	if err != nil {
		return false, err
	}
	a = strings.ToLower(a)
	return a == "y" || a == "yes", nil
}

// tildePath shows a path below the home folder as ~/…, the way people know it.
func tildePath(home, p string) string {
	if home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if rel, err := filepath.Rel(home, p); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		return "~" + string(filepath.Separator) + rel
	}
	return p
}

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
