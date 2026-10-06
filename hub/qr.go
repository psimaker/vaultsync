package main

import (
	"fmt"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// renderQR draws content as a QR code for a terminal: Unicode upper half
// blocks, two module rows per text row (the upper module is the foreground
// color, the lower one the background). The colors are explicit ANSI black
// on bright white, quiet zone included, so the code scans on dark and light
// terminal themes alike — `setup.sh` runs `code` without a TTY, so there is
// nothing to detect a theme from. Medium error correction absorbs the gaps
// some terminals leave between rows.
func renderQR(content string) (string, error) {
	q, err := qrcode.New(content, qrcode.Medium)
	if err != nil {
		return "", err
	}
	return renderModules(q.Bitmap()), nil // Bitmap includes the 4-module quiet zone
}

const (
	ansiDarkFG  = 30  // black
	ansiLightFG = 97  // bright white
	ansiDarkBG  = 40  // black
	ansiLightBG = 107 // bright white
	ansiReset   = "\x1b[0m"
)

func renderModules(dark [][]bool) string {
	var b strings.Builder
	for y := 0; y < len(dark); y += 2 {
		b.WriteString("  ")
		fg, bg := -1, -1
		for x := range dark[y] {
			wantFG, wantBG := ansiLightFG, ansiLightBG
			if dark[y][x] {
				wantFG = ansiDarkFG
			}
			if y+1 < len(dark) && dark[y+1][x] {
				wantBG = ansiDarkBG
			}
			if wantFG != fg || wantBG != bg {
				fmt.Fprintf(&b, "\x1b[%d;%dm", wantFG, wantBG)
				fg, bg = wantFG, wantBG
			}
			b.WriteString("▀")
		}
		b.WriteString(ansiReset + "\n")
	}
	return b.String()
}
