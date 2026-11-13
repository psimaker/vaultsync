package pairing

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// FuzzIssue319_ErrorText holds what errorText promises against any bytes an
// answer can bring (decision 062). The seeds run with every `go test`;
// `go test ./pairing -run '^$' -fuzz FuzzIssue319_ErrorText` searches on.
func FuzzIssue319_ErrorText(f *testing.F) {
	for _, seed := range []string{
		"",
		"pairing code rejected",
		"the pairing code was locked after repeated wrong attempts — run `vaultsync-hub code` for a new one",
		`no vault named "My  Notes" on this hub`,
		"path overlaps an existing vault (Journal\u30002026\u00a0Notes)",
		"\x1b[2J\u202ethe pairing code was locked\u2028after repeated\r\nwrong\tattempts\u0085\u009b31m \x07",
		" \t\u200d \u200c\r\n",
		strings.Repeat("\u200d", 100) + "x",
		strings.Repeat("я", 200),
		strings.Repeat("a", 296) + " " + strings.Repeat("b", 10),
		strings.Repeat("a", 295) + "\u3000\u00a0" + strings.Repeat("b", 10),
		"a flag \U0001f3f4\U000e0067\U000e0062\U000e0073\U000e0063\U000e0074\U000e007f and tags \U000e0061\U000e0062 without one",
		strings.Repeat("\U0001f3f4\U000e0067\U000e0062", 40),
		"\xff\xfe no text \xc3",
		"a \u2029 b\v\fc\u200b ",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		out := errorText(text)
		if !utf8.ValidString(out) || len(out) > maxErrorText {
			t.Fatalf("%+q: %d bytes, or no valid text: %+q", text, len(out), out)
		}
		if out != strings.TrimSpace(out) {
			t.Fatalf("%+q: white space at an end: %+q", text, out)
		}
		// Nothing a name would lose, and of the format characters only the
		// two kinds a name keeps.
		if withoutHidden(out) != out {
			t.Fatalf("%+q: keeps what a name loses: %+q", text, out)
		}
		for _, r := range out {
			if unicode.IsControl(r) || unicode.In(r, unicode.Zl, unicode.Zp) || unicode.Is(unicode.Cf, r) && !joiner(r) && !flagTag(r) {
				t.Fatalf("%+q: %U is kept: %+q", text, r, out)
			}
		}
		if again := errorText(out); again != out {
			t.Fatalf("%+q: cleaned twice it reads %+q, once %+q", text, again, out)
		}
		// A text that needs no cleaning arrives byte for byte.
		if needsNoCleaning(text) && out != text {
			t.Fatalf("a clean text of %d bytes arrived changed: %+q, was %+q", len(text), out, text)
		}
	})
}

// needsNoCleaning reports a text errorText has to hand on as it is: valid
// UTF-8 within the bound, nothing hidden in it but joiners, no white space
// at an end, and something to show.
func needsNoCleaning(text string) bool {
	if !utf8.ValidString(text) || len(text) > maxErrorText || text != strings.TrimSpace(text) {
		return false
	}
	shows := false
	for _, r := range text {
		if r == 0x200c || r == 0x200d {
			continue
		}
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return false
		}
		shows = shows || !unicode.IsSpace(r)
	}
	return shows
}
