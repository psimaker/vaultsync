package main

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/psimaker/vaultsync/hub/pairing"
	qrcode "github.com/skip2/go-qrcode"
)

var ansiSGR = regexp.MustCompile(`^\x1b\[(\d+);(\d+)m`)

// parseRendered reads renderQR's output back into dark/light modules, so the
// test checks what a terminal shows, not what the renderer meant to draw.
func parseRendered(t *testing.T, out string) [][]bool {
	t.Helper()
	var rows [][]bool
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, "  ") || !strings.HasSuffix(line, ansiReset) {
			t.Fatalf("line %d is not indented or not reset: %q", i, line)
		}
		line = strings.TrimSuffix(strings.TrimPrefix(line, "  "), ansiReset)
		var top, bottom []bool
		fg, bg := -1, -1
		for line != "" {
			if m := ansiSGR.FindStringSubmatch(line); m != nil {
				fg, _ = strconv.Atoi(m[1])
				bg, _ = strconv.Atoi(m[2])
				line = line[len(m[0]):]
				continue
			}
			if !strings.HasPrefix(line, "▀") {
				t.Fatalf("line %d: unexpected text %q", i, line)
			}
			if (fg != ansiDarkFG && fg != ansiLightFG) || (bg != ansiDarkBG && bg != ansiLightBG) {
				t.Fatalf("line %d: module drawn without explicit colors (fg %d, bg %d)", i, fg, bg)
			}
			top = append(top, fg == ansiDarkFG)
			bottom = append(bottom, bg == ansiDarkBG)
			line = strings.TrimPrefix(line, "▀")
		}
		rows = append(rows, top, bottom)
	}
	return rows
}

// The Hub prints the pairing link as a QR next to the code (#174). Every
// module must reach the terminal with an explicit color, the quiet zone
// included, and the drawing must be exactly the encoder's matrix.
func TestIssue174_RenderQRMatchesTheEncodedMatrix(t *testing.T) {
	link := pairing.Link("ENVELOPE-MUSHROOM-07", "192.168.178.120:8390")
	out, err := renderQR(link)
	if err != nil {
		t.Fatal(err)
	}
	q, err := qrcode.New(link, qrcode.Medium)
	if err != nil {
		t.Fatal(err)
	}
	want := q.Bitmap()
	got := parseRendered(t, out)
	if len(want)%2 == 1 {
		want = append(want, make([]bool, len(want[0]))) // the last text row's lower half is light
	}
	if len(got) != len(want) {
		t.Fatalf("rows: got %d, want %d", len(got), len(want))
	}
	for y := range want {
		if len(got[y]) != len(want[y]) {
			t.Fatalf("row %d: width %d, want %d", y, len(got[y]), len(want[y]))
		}
		for x := range want[y] {
			if got[y][x] != want[y][x] {
				t.Fatalf("module (%d,%d) differs", x, y)
			}
		}
	}
	// The encoder's matrix carries the four-module quiet zone the camera
	// needs; the renderer must not crop it.
	size := len(q.Bitmap())
	for i := range size {
		for b := range 4 {
			if got[b][i] || got[size-1-b][i] || got[i][b] || got[i][size-1-b] {
				t.Fatalf("quiet zone not light at %d/%d", i, b)
			}
		}
	}
}

func TestIssue174_AdvertisedAddress(t *testing.T) {
	for in, want := range map[string]string{
		"192.168.1.20":      "192.168.1.20:9000",
		"192.168.1.20:8390": "192.168.1.20:8390",
		"[fd00::5]":         "[fd00::5]:9000",
		"fd00::5":           "[fd00::5]:9000",
		" 10.0.0.2 ":        "10.0.0.2:9000",
	} {
		got, err := advertisedAddress(in, 9000)
		if err != nil || got != want {
			t.Errorf("advertisedAddress(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"8.8.8.8", "hub.example.com", "192.168.1.20:", "http://192.168.1.20"} {
		if _, err := advertisedAddress(in, 9000); err == nil {
			t.Errorf("advertisedAddress(%q) accepted", in)
		}
	}
	// Without --address the hint is private or absent — never a public address.
	got, err := advertisedAddress("", 9000)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		if _, err := pairing.LocalHubAddress(got); err != nil {
			t.Fatalf("default hint %q is not local: %v", got, err)
		}
	}
}
