package pairing

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// #320: a vault's name — a label from a Hub, and the name typed for a new
// vault — loses what hides, as a Hub's or a device's name does (061), and is
// never cut: it is counted in characters against MaxVaultName and refused
// past it by whoever takes it (decision 089).

func r(cp ...rune) string { return string(cp) }

// scotland is the flag of Scotland: U+1F3F4, the tags g b s c t, and the
// cancel tag.
var scotland = r(0x1f3f4, 0xe0067, 0xe0062, 0xe0073, 0xe0063, 0xe0074, 0xe007f)

func TestIssue320_CleanLabelDropsWhatHidesAndCutsNothing(t *testing.T) {
	long := strings.Repeat("n", MaxVaultName+1)
	umlauts := "Notizen " + strings.Repeat("ü", 30)
	cjk := strings.Repeat("笔", 40)
	cases := []struct{ in, want string }{
		{"Notes", "Notes"},
		{"  My  Notes \t", "My  Notes"},
		// Past 64 bytes, and past 64 characters: kept whole.
		{long, long},
		{umlauts, umlauts},
		{cjk, cjk},
		// What hides goes: a zero-width space, a mark that reverses, a line
		// separator, a control that starts a terminal sequence, a soft
		// hyphen, a byte order mark, a direction mark.
		{"Notes" + r(0x200b), "Notes"},
		{r(0x202e) + "Home", "Home"},
		{"a" + r(0x2028) + "b", "ab"},
		{r(0x9b) + "31m" + "Red", "31mRed"},
		{"No" + r(0xad) + "tes", "Notes"},
		{r(0xfeff) + "Notes", "Notes"},
		{"Notes" + r(0x200e), "Notes"},
		// Dropped first, trimmed after: the space it stood beside goes too.
		{r(0x200f) + " Notes " + r(0x200f), "Notes"},
		// What a name is written with stays.
		{"a" + r(0x200d) + "b", "a" + r(0x200d) + "b"},
		{"x" + r(0x200c) + "y", "x" + r(0x200c) + "y"},
		{scotland + " Trip", scotland + " Trip"},
		// Nothing to show: no name.
		{" " + r(0x200c) + " " + r(0x200d), ""},
		{r(0x200b), ""},
		{"", ""},
		// Bytes that are no UTF-8 come out as U+FFFD.
		{"a\xffb", "a" + r(0xfffd) + "b"},
	}
	for _, c := range cases {
		if got := CleanLabel(c.in); got != c.want {
			t.Errorf("CleanLabel(%+q) = %+q, want %+q", c.in, got, c.want)
		}
	}
	if MaxVaultName != 64 {
		t.Fatalf("MaxVaultName = %d: the Hub has taken 64 characters since its first release", MaxVaultName)
	}
}

// sanitizeNameBefore320 is SanitizeName as it was before #320 built it from
// CleanLabel. The fuzz target holds the two equal: a Hub's and a device's
// name are cleaned and cut as before.
func sanitizeNameBefore320(name string) string {
	out := strings.TrimSpace(withoutHidden(name))
	out = strings.TrimSpace(cutTo(out, 64))
	if drawsNothing(out) {
		return ""
	}
	return out
}

func FuzzIssue320_CleanLabel(f *testing.F) {
	for _, s := range []string{
		"", "Notes", " Notes ", strings.Repeat("ü", 40), strings.Repeat("n", 70),
		strings.Repeat("笔", 30) + r(0x200b), r(0x202e) + "Home", "a" + r(0x2028) + "b",
		scotland + scotland, r(0x1f3f4) + r(0xe0067), strings.Repeat(r(0x200d), 40) + "x",
		strings.Repeat(" ", 63) + "é", "a\xffb", r(0x9b, 0x85, 0x7f),
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if got, want := SanitizeName(s), sanitizeNameBefore320(s); got != want {
			t.Fatalf("SanitizeName(%+q) = %+q, before #320 %+q", s, got, want)
		}
		c := CleanLabel(s)
		if !utf8.ValidString(c) {
			t.Fatalf("CleanLabel(%+q) = %+q is no valid text", s, c)
		}
		if CleanLabel(c) != c || withoutHidden(c) != c {
			t.Fatalf("CleanLabel(%+q) = %+q still holds what it drops", s, c)
		}
		if c != strings.TrimSpace(c) || (c != "" && drawsNothing(c)) {
			t.Fatalf("CleanLabel(%+q) = %+q begins or ends with a space, or shows nothing", s, c)
		}
		// Nothing is cut: a Hub's or a device's name is the label's first
		// 64 bytes, and the label is everything that does not hide.
		if n := SanitizeName(s); !strings.HasPrefix(c, n) {
			t.Fatalf("SanitizeName(%+q) = %+q is not where CleanLabel's %+q begins", s, n, c)
		}
		if drawsNothing(strings.TrimSpace(withoutHidden(s))) != (c == "") {
			t.Fatalf("CleanLabel(%+q) = %+q drops or keeps a name it should not", s, c)
		}
	})
}
