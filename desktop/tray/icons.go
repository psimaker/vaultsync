package tray

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"sync"
)

// The icons: five shapes, drawn here at start (no image files to ship or
// to go stale), 32×32 pixels for a 16-point menu-bar icon at 2×. On a Mac
// the template variant — black, the alpha channel is the shape — takes the
// menu bar's own colour; Linux panels get a mid grey that reads on light
// and dark bars alike.

const iconSize = 32

var (
	iconMu    sync.Mutex
	iconCache = map[bool]map[Glyph][]byte{} // template → glyph → PNG
)

// Icon is the PNG for g: the template variant for a Mac's menu bar, else
// the grey one.
func Icon(g Glyph, template bool) []byte {
	iconMu.Lock()
	defer iconMu.Unlock()
	if iconCache[template] == nil {
		iconCache[template] = map[Glyph][]byte{}
	}
	if b, ok := iconCache[template][g]; ok {
		return b
	}
	b := drawIcon(g, template)
	iconCache[template][g] = b
	return b
}

// drawIcon rasterizes the shape with 4×4 supersampling.
func drawIcon(g Glyph, template bool) []byte {
	const ss = 4
	img := image.NewNRGBA(image.Rect(0, 0, iconSize, iconSize))
	ink := color.NRGBA{R: 0x8e, G: 0x8e, B: 0x8e}
	if template {
		ink = color.NRGBA{}
	}
	for y := 0; y < iconSize; y++ {
		for x := 0; x < iconSize; x++ {
			inside := 0
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					px := float64(x) + (float64(sx)+0.5)/ss
					py := float64(y) + (float64(sy)+0.5)/ss
					if shape(g, px, py) {
						inside++
					}
				}
			}
			if inside == 0 {
				continue
			}
			c := ink
			c.A = uint8(255 * inside / (ss * ss))
			img.SetNRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// shape says whether the point (x, y) on the 32×32 canvas is inked for g.
func shape(g Glyph, x, y float64) bool {
	const cx, cy = 16.0, 16.0
	dx, dy := x-cx, y-cy
	r := math.Hypot(dx, dy)
	switch g {
	case GlyphSynced:
		// A disc with a check mark cut out of it.
		return r <= 12 && !checkMark(x, y)
	case GlyphSyncing:
		// A ring with a gap: a circle still being drawn.
		if r > 12 || r < 8 {
			return false
		}
		angle := math.Atan2(-dy, dx) // counter-clockwise, 0 to the right
		return !(angle > 0.3 && angle < 1.3)
	case GlyphPaused:
		// Two bars.
		return math.Abs(dy) <= 9 && (math.Abs(dx-5) <= 2.5 || math.Abs(dx+5) <= 2.5)
	case GlyphAttention:
		// A disc with an exclamation mark cut out of it.
		if r > 12 {
			return false
		}
		bar := math.Abs(dx) <= 1.8 && dy >= -7.5 && dy <= 2.5
		dot := math.Hypot(dx, dy-6.2) <= 2
		return !bar && !dot
	}
	// Offline: a ring with a slash across it.
	ring := r <= 12 && r >= 8.5
	slash := math.Abs(dx+dy) <= 2.2 && r <= 12
	return ring || slash
}

// checkMark is the stroke of a check mark around the centre: two
// segments, each a thick line.
func checkMark(x, y float64) bool {
	return nearSegment(x, y, 9, 16.5, 14, 21.5, 2.2) || nearSegment(x, y, 14, 21.5, 23, 11, 2.2)
}

// nearSegment: the point is within w of the segment (x1,y1)–(x2,y2).
func nearSegment(x, y, x1, y1, x2, y2, w float64) bool {
	vx, vy := x2-x1, y2-y1
	wx, wy := x-x1, y-y1
	t := (wx*vx + wy*vy) / (vx*vx + vy*vy)
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	return math.Hypot(x-(x1+t*vx), y-(y1+t*vy)) <= w
}
