package tray

import (
	"strings"
	"unicode"
)

// The menu's longer texts: a notice, and the line under the headline. A menu
// item is one line however long its text; macOS cuts a long one in its
// middle, where a remedy often stands, and widens the whole menu for it
// (#291). Such a text is shown on a few short lines instead, and a
// notification carries it whole.

const (
	// MenuLineWidth is how many characters a line of the menu holds.
	MenuLineWidth = 60
	// MenuLinesMost is how many lines a notice, or the line under the
	// headline, takes at most.
	MenuLinesMost = 4
	// notifyMost bounds a notification's text, in characters: every text
	// the menu says fits, and one from outside (an agent's refusal) is cut.
	notifyMost = 400
	// cutMark ends a line after which something was left out.
	cutMark = "…"
)

// MenuLines is text as the menu shows it.
//
// Each paragraph is cleaned as any text from outside (oneLine). It is broken
// at its spaces into lines of at most MenuLineWidth characters; a word longer
// than a line, such as a path, is broken where it must, never inside a
// character (clusters).
//
// At most MenuLinesMost lines are shown. Where more was said, the middle goes:
// the first lines, the last of them ending in "…", and the longest end of the
// text that fits in the lines left. That end starts at a sentence or clause:
// after ". ", "; ", " — " or ": ", or at a paragraph. A text that says its
// cause and then its remedy keeps its remedy that way. Then every line's
// tooltip holds the whole text.
func MenuLines(text string) []Line {
	pars := paragraphs(text)
	var lines []string
	for _, p := range pars {
		lines = append(lines, breakLine(p, MenuLineWidth)...)
	}
	if len(lines) <= MenuLinesMost {
		return asLines(lines, "")
	}
	return asLines(shortened(pars), strings.Join(pars, "\n"))
}

// notificationText is text for a notification, which leaves this app for the
// system's notification service: on one line, cleaned as any text from
// outside, at most notifyMost characters.
func notificationText(text string) string {
	s := strings.Join(paragraphs(text), " ")
	c := clusters(s)
	if len(c) <= notifyMost {
		return s
	}
	return marked(c[:notifyMost], notifyMost)
}

// paragraphs is text's paragraphs, each cleaned (oneLine), the empty ones left
// out.
func paragraphs(text string) []string {
	var pars []string
	for _, p := range strings.Split(text, "\n") {
		if p = oneLine(p); p != "" {
			pars = append(pars, p)
		}
	}
	return pars
}

func asLines(lines []string, tooltip string) []Line {
	out := make([]Line, len(lines))
	for i, l := range lines {
		out[i] = Line{Text: l, Tooltip: tooltip}
	}
	return out
}

// tailMarks are where the end of a text may begin: after the end of a
// sentence or a clause.
var tailMarks = []string{". ", "; ", " — ", ": "}

// shortened is pars on MenuLinesMost lines: the first lines and the longest
// end that fits in the lines left, at least one line before it. Without such
// an end, the first MenuLinesMost lines.
func shortened(pars []string) []string {
	for k, p := range pars {
		// The start of this paragraph, then each mark in it, from the first:
		// the first end that fits is the longest.
		starts := []int{}
		if k > 0 {
			starts = append(starts, 0)
		}
		for i := 0; i < len(p); {
			at, mark := nextMark(p[i:])
			if at < 0 {
				break
			}
			starts = append(starts, i+at+len(mark))
			i += at + len(mark)
		}
		for _, s := range starts {
			tail := append([]string{strings.TrimSpace(p[s:])}, pars[k+1:]...)
			var tailLines []string
			for _, t := range tail {
				tailLines = append(tailLines, breakLine(t, MenuLineWidth)...)
			}
			if len(tailLines) == 0 || len(tailLines) > MenuLinesMost-1 {
				continue
			}
			head := append(append([]string{}, pars[:k]...), strings.TrimSpace(p[:s]))
			var headLines []string
			for _, h := range head {
				headLines = append(headLines, breakLine(h, MenuLineWidth)...)
			}
			keep := MenuLinesMost - len(tailLines)
			if len(headLines) == 0 {
				continue
			}
			if len(headLines) > keep {
				headLines = headLines[:keep]
			}
			headLines[len(headLines)-1] = marked(clusters(headLines[len(headLines)-1]), MenuLineWidth)
			return append(headLines, tailLines...)
		}
	}
	var lines []string
	for _, p := range pars {
		lines = append(lines, breakLine(p, MenuLineWidth)...)
	}
	lines = lines[:MenuLinesMost]
	lines[MenuLinesMost-1] = marked(clusters(lines[MenuLinesMost-1]), MenuLineWidth)
	return lines
}

// nextMark finds the first of tailMarks in s: where, and which.
func nextMark(s string) (int, string) {
	at, which := -1, ""
	for _, m := range tailMarks {
		if i := strings.Index(s, m); i >= 0 && (at < 0 || i < at) {
			at, which = i, m
		}
	}
	return at, which
}

// marked is the line c with cutMark at its end, within width characters:
// what does not fit goes, at a space where there is one.
func marked(c []string, width int) string {
	if len(c)+1 > width {
		c = c[:width-1]
		for i := len(c) - 1; i > 0; i-- {
			if c[i] == " " {
				c = c[:i]
				break
			}
		}
	}
	return strings.TrimRight(strings.Join(c, ""), " ") + cutMark
}

// breakLine breaks s at its spaces into lines of at most width characters. A
// word longer than a line is broken where it must, between two characters.
func breakLine(s string, width int) []string {
	var lines []string
	var line []string
	for _, word := range strings.Split(s, " ") {
		if word == "" {
			continue
		}
		w := clusters(word)
		need := len(w)
		if len(line) > 0 {
			need++
		}
		if len(line)+need <= width {
			if len(line) > 0 {
				line = append(line, " ")
			}
			line = append(line, w...)
			continue
		}
		if len(line) > 0 {
			lines = append(lines, strings.Join(line, ""))
			line = nil
		}
		for len(w) > width {
			lines = append(lines, strings.Join(w[:width], ""))
			w = w[width:]
		}
		line = w
	}
	if len(line) > 0 {
		lines = append(lines, strings.Join(line, ""))
	}
	return lines
}

// clusters splits s into what reads as one character, so that no line is
// broken inside one: a letter with the marks that combine with it, an emoji
// with its variation selector or skin tone, two regional indicators that make
// a flag, and what a zero-width joiner joins. Text the menu shows has lost
// its joiners and tags already (oneLine); the rule keeps them all the same.
func clusters(s string) []string {
	var out []string
	var prev rune
	lone := false // the last cluster is one regional indicator alone
	for _, r := range s {
		switch {
		case len(out) == 0:
		case joinsPrevious(r), prev == 0x200D:
			out[len(out)-1] += string(r)
			prev = r
			continue
		case regional(r) && lone:
			out[len(out)-1] += string(r)
			prev, lone = r, false
			continue
		}
		out = append(out, string(r))
		prev, lone = r, regional(r)
	}
	return out
}

// joinsPrevious reports a character that belongs to the one before it: a
// combining or spacing mark (variation selectors among them), an emoji's skin
// tone, a zero-width joiner, an emoji tag.
func joinsPrevious(r rune) bool {
	return unicode.In(r, unicode.Mn, unicode.Me, unicode.Mc) ||
		(r >= 0x1F3FB && r <= 0x1F3FF) || r == 0x200D || (r >= 0xE0020 && r <= 0xE007F)
}

// regional reports a regional indicator: two make a flag.
func regional(r rune) bool { return r >= 0x1F1E6 && r <= 0x1F1FF }
