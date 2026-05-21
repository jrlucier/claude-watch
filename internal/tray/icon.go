package tray

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
)

// iconHeight is the side length we render at. Kept close to common panel sizes
// (22-32px) on purpose: rendering at 128 and letting the panel downsample
// 5.8× washed out the anti-aliased strokes (every edge pixel became 50% gray,
// so "white" text read as gray). At 64 the panel only halves on a 22-pixel
// panel, and on a 32-pixel panel it's near-native.
const iconHeight = 64

// Severity bands match Haletran/claude-usage-extension extension.js:369-377.
type Severity int

const (
	SevGreen Severity = iota
	SevYellow
	SevOrange
	SevRed
	SevUnknown // no data — render in gray
)

// SeverityFor maps a utilization percentage to a severity band.
func SeverityFor(util float64) Severity {
	switch {
	case util >= 90:
		return SevRed
	case util >= 70:
		return SevOrange
	case util >= 40:
		return SevYellow
	default:
		return SevGreen
	}
}

var sevColor = map[Severity]color.NRGBA{
	SevGreen:   {0x22, 0xc5, 0x5e, 0xff},
	SevYellow:  {0xea, 0xb3, 0x08, 0xff},
	SevOrange:  {0xf9, 0x73, 0x16, 0xff},
	SevRed:     {0xef, 0x44, 0x44, 0xff},
	SevUnknown: {0x9c, 0x9c, 0x9c, 0xff},
}

// digitColor is the on-icon text color. White; severity is conveyed by the
// corner dot, not the digit.
var digitColor = color.NRGBA{0xff, 0xff, 0xff, 0xff}

// digitOutline is painted as a 1px stroke around the white digits so they
// stay legible on light panels (where pure-white text would otherwise blend
// into the background). Translucent so it doesn't add visual weight on the
// far more common dark panels.
var digitOutline = color.NRGBA{0x00, 0x00, 0x00, 0xa8}

// RenderBars produces the tray icon: a single big number (the 5-hour
// utilization percentage) in neutral text, plus a colored severity dot in
// the bottom-right corner — the dot is what tells the user how urgent it is.
//
//	fiveH:  percentage 0-100 (clamped).
//	sevenD: accepted for API symmetry but unused in the icon.
//	stale:  when true, the digit & dot dim to indicate the API hasn't refreshed.
//	hasAPI: when false, the icon renders "?" in gray — there's no value to show.
func RenderBars(fiveH, _ float64, stale, hasAPI bool) ([]byte, error) {
	img := image.NewNRGBA(image.Rect(0, 0, iconHeight, iconHeight))

	var label string
	var sev Severity
	if !hasAPI {
		label = "?"
		sev = SevUnknown
	} else {
		pct := int(fiveH + 0.5)
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
		label = pctLabel(pct)
		sev = SeverityFor(fiveH)
	}

	textCol := digitColor
	dotCol := sevColor[sev]
	// Dimming signals "data is old". When we have no data at all the icon
	// already shows "?" — dimming on top would just make the glyph hard to
	// read. So only dim when hasAPI && stale.
	if stale && hasAPI {
		textCol = dim(textCol)
		dotCol = dim(dotCol)
	}

	// Layout: digit centered, with a colored underline at the bottom of the
	// canvas as the severity indicator. This keeps the digit at full size
	// while still surfacing the color signal.
	const lineH = 8      // underline thickness, px
	const lineMargin = 6 // px clear on left and right
	const lineBottom = 3 // px between underline and canvas bottom

	// Lock the font size to what's right for a 2-digit value ("88" is the
	// widest pair). Single-digit and "?" then render at the same height as
	// a 2-digit reading. The full width is available for digits; the only
	// vertical reservation is the underline plus padding.
	ref := label
	if len(label) < 2 {
		ref = "88"
	}
	digitW := iconHeight - 4
	digitH := iconHeight - (lineH + lineBottom + 2)
	// 0.92× shaves a couple of pixels off the max-fit height so the digits
	// don't push right up against the icon edges; it reads as more
	// considered than "as big as possible."
	size := fitFontSize(ref, digitW, digitH) * 0.92
	outline := digitOutline
	if stale && hasAPI {
		outline = dim(outline)
	}
	textW := drawTextWidth(label, size)
	textX := (iconHeight - textW) / 2
	// Shift the visual centre of the text upward by half the reserved
	// underline strip so the digit sits centred within the *remaining*
	// canvas, not the whole canvas — otherwise it visually drifts down
	// against the line.
	drawTextOffsetY(img, label, size, textX, -(lineH+lineBottom)/2, textCol, outline)

	drawUnderline(img, lineMargin, iconHeight-lineBottom-lineH, iconHeight-lineMargin, iconHeight-lineBottom, dotCol)

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// pctLabel formats the percentage. 100 shows "!!" — two characters at the
// locked font size, matching the visual weight of 2-digit values, and
// "you're maxed out" is the only message that matters at saturation.
func pctLabel(p int) string {
	if p >= 100 {
		return "!!"
	}
	if p < 10 {
		return digit(p)
	}
	return digit(p/10) + digit(p%10)
}

func digit(n int) string { return string(rune('0' + n)) }

// drawUnderline paints a filled horizontal bar with semi-rounded ends. The
// rounding is just a 1-pixel chamfer at each corner — enough to read as
// "rounded" at panel scale without burning cycles on full anti-aliased
// quarter-circles.
func drawUnderline(img *image.NRGBA, x0, y0, x1, y1 int, c color.NRGBA) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			// Skip the four corner pixels for a soft chamfer.
			leftCorner := x == x0 && (y == y0 || y == y1-1)
			rightCorner := x == x1-1 && (y == y0 || y == y1-1)
			if leftCorner || rightCorner {
				continue
			}
			blend(img, x, y, c)
		}
	}
}

// drawDot paints an anti-aliased filled circle of radius r centered at (cx,cy)
// using source-over blending.
func drawDot(img *image.NRGBA, cx, cy, r int, c color.NRGBA) {
	for y := cy - r - 1; y <= cy+r+1; y++ {
		for x := cx - r - 1; x <= cx+r+1; x++ {
			dx := float64(x - cx)
			dy := float64(y - cy)
			d := math.Sqrt(dx*dx + dy*dy)
			if d > float64(r)+0.5 {
				continue
			}
			alpha := c.A
			if d > float64(r)-0.5 {
				// Anti-aliased edge ring.
				alpha = uint8(float64(c.A) * (float64(r) + 0.5 - d))
			}
			ac := c
			ac.A = alpha
			blend(img, x, y, ac)
		}
	}
}

// blend writes the given NRGBA pixel into img with source-over compositing.
func blend(img *image.NRGBA, x, y int, c color.NRGBA) {
	b := img.Bounds()
	if x < b.Min.X || y < b.Min.Y || x >= b.Max.X || y >= b.Max.Y {
		return
	}
	i := (y-b.Min.Y)*img.Stride + (x-b.Min.X)*4
	dst := color.NRGBA{img.Pix[i+0], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3]}
	sa := float64(c.A) / 255
	da := float64(dst.A) / 255 * (1 - sa)
	outA := sa + da
	if outA <= 0 {
		return
	}
	out := func(s, d uint8) uint8 {
		v := (float64(s)*sa + float64(d)*da) / outA
		if v > 255 {
			v = 255
		}
		return uint8(v)
	}
	img.Pix[i+0] = out(c.R, dst.R)
	img.Pix[i+1] = out(c.G, dst.G)
	img.Pix[i+2] = out(c.B, dst.B)
	img.Pix[i+3] = uint8(outA * 255)
}

func dim(c color.NRGBA) color.NRGBA {
	return color.NRGBA{
		R: uint8(int(c.R) * 60 / 100),
		G: uint8(int(c.G) * 60 / 100),
		B: uint8(int(c.B) * 60 / 100),
		A: c.A,
	}
}
