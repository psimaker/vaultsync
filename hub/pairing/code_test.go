package pairing

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func TestWordListIsFixedAndClean(t *testing.T) {
	seen := map[string]bool{}
	for i, w := range wordList {
		if w == "" || w != strings.ToLower(w) || strings.ContainsAny(w, " -_0123456789") {
			t.Fatalf("word %d %q is not a plain lower-case word", i, w)
		}
		if len(w) < 3 || len(w) > 9 {
			t.Fatalf("word %q has an awkward length", w)
		}
		if seen[w] {
			t.Fatalf("duplicate word %q", w)
		}
		seen[w] = true
	}
}

func TestGenerateCodeIsCanonical(t *testing.T) {
	for range 50 {
		code, err := GenerateCode()
		if err != nil {
			t.Fatal(err)
		}
		norm, err := NormalizeCode(code)
		if err != nil {
			t.Fatalf("generated code %q does not normalize: %v", code, err)
		}
		if norm != code {
			t.Fatalf("generated %q, normalized %q", code, norm)
		}
		parts := strings.Split(code, "-")
		if len(parts) != 3 || len(parts[2]) != 2 {
			t.Fatalf("unexpected shape %q", code)
		}
	}
}

func TestNormalizeCodeForgivesHumanInput(t *testing.T) {
	cases := map[string]string{
		"TULIP-ANCHOR-07":     "TULIP-ANCHOR-07",
		"tulip anchor 7":      "TULIP-ANCHOR-07",
		"  Tulip_Anchor.42  ": "TULIP-ANCHOR-42",
		"tulip--anchor--00":   "TULIP-ANCHOR-00",
	}
	for in, want := range cases {
		got, err := NormalizeCode(in)
		if err != nil || got != want {
			t.Errorf("NormalizeCode(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestNormalizeCodeRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "tulip", "tulip-anchor", "tulip-anchor-123", "tulip-anchor-x1", "zzzz-anchor-01", "tulip-anchor-01-extra", "TULIP ANCHOR"} {
		if _, err := NormalizeCode(in); !errors.Is(err, ErrInvalidCode) {
			t.Errorf("NormalizeCode(%q) accepted: %v", in, err)
		}
	}
}

// The word list must never change once shipped: codes are not versioned, so a
// reordered or edited list would silently turn every issued code into a
// different password scalar. Pinned when the list moved into this package
// (#174); the hash is of the list as it shipped in hub-v0.1.0.
func TestIssue174_WordListUnchanged(t *testing.T) {
	sum := sha256.Sum256([]byte(strings.Join(wordList[:], ",")))
	const shipped = "a803f2cd6c5dfc92f7e7bd40b335147252800bbe2b3f144bb50ce8337145fb26"
	if got := hex.EncodeToString(sum[:]); got != shipped {
		t.Fatalf("word list changed: sha256 %s, shipped %s", got, shipped)
	}
}
